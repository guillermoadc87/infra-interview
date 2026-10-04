# Path 4 — CI/CD & GitOps

The exercise shipped a `setup.sh` that ran `docker build` and four
`helm upgrade --install` commands against one cluster. That is a deployment
*script*, not a deployment *system*: nothing recorded what should be running, so
nothing could detect drift, promote a version, or roll one back.

This replaces it with a pull-based GitOps pipeline across four clusters — a hub
running Argo CD, and dev / staging / prod spokes.

Detail lives in [`SOLUTION.md`](SOLUTION.md) (decisions and trade-offs),
[`gitops/README.md`](gitops/README.md) (the config model), and
[`docs/verification-log.md`](docs/verification-log.md) (raw evidence).

---

## 1. Argo CD, in three levels

```
gitops/root/root-application.yaml    LEVEL 1   applied by hand — once, ever
gitops/appsets/*.yaml                LEVEL 2   ApplicationSets
gitops/apps/**  gitops/platform/**   LEVEL 3   the manifests themselves
```

**Level 1** is an App-of-Apps and the only imperative step in the system.
Something has to install the thing that installs everything else. After it, the
only supported way to change any cluster is a git commit.

**Level 2** is where the design lives. Each ApplicationSet is a **matrix
generator**:

- a **git** child discovers *what* to deploy by walking `gitops/apps/*/envs/*`
- a **clusters** child discovers *where*, by matching an `env` label

The environment is **read out of the repo path** — for
`gitops/apps/api-service/envs/dev`, segment 2 is the application and segment 4 is
the environment. That is what lets one ApplicationSet span every environment
instead of one file per environment.

**The payoff: a cluster provisions itself.** `./scripts/register-spoke.sh dev`
writes a single Secret labelled `env=dev`, and every Application with a dev
overlay appears on it. There is no list anywhere of what to install per
environment — the label *is* the contract.

Four ApplicationSets: `apps`, `platform-helm`, `platform-kustomize`, and
`pr-preview`, plus AppProjects for boundary enforcement.

---

## 2. Config, split by how it behaves under promotion

Not by what it looks like. Four categories, each with exactly one home:

| # | What | Where | Promoted? |
|---|---|---|---|
| 1 | Image version | `envs/<env>/kustomization.yaml` → `images[].newTag` | **yes** |
| 2 | Kubernetes settings (replicas, resources, PDB) | `envs/<env>/replicas.yaml`, base | no |
| 3 | Environment identity + tier (`ENVIRONMENT`, `PAYMENTS_URL`, `LOG_LEVEL`) | `variants/env/*`, `variants/tier/*` | **never** |
| 4 | Business settings validated with the build | `envs/<env>/settings.yaml` | **yes**, verbatim copy |

Category 3 sits **physically outside `envs/`**, and promotion only ever touches
`images[].newTag` and `envs/<env>/settings.yaml`.

So the guarantee is structural, not a convention: **there is no file you can copy
that would move the sandbox payment endpoint into production.** Nothing under
`variants/` is reachable from a promotion, and no file under `envs/` names
`PAYMENTS_URL`.

---

## 3. Promotion

**It retags a digest. It never rebuilds.** A rebuild from the same commit is not
the same artifact — base images move, module proxies drift. "It passed staging"
only means something if prod runs the same bytes. The workflow asserts the digest
is unchanged and refuses an image with no `linux/arm64` manifest.

**You do not tell it which tag to promote.** Image Updater writes the deployed tag
back into git, so `envs/<env>/kustomization.yaml` *is* the record of what an
environment runs. Promotion reads it; rollback reads the same file's history. You
pick a service and a target and press Run. Resolution is per-service, because the
services build independently and their tags legitimately differ.

That is stricter than the form it replaced, which accepted any well-formed tag —
including one from an unrelated commit.

```
dev ──────────▶ staging ──────────▶ prod
      retag           retag + PR
```

- **staging** is watched by Image Updater, so the retag alone is enough.
- **prod is watched by nothing, deliberately.** Nothing can move its tag except a
  reviewed pull request. It auto-syncs like every other environment, so **merging
  that PR is the deploy.**

**Rollback rolls forward.** `git revert` does not roll back dev or staging: Image
Updater would find the bad tag still newest and write it straight back. So a
rollback republishes a known-good digest under a newer tag. On prod, where nothing
is racing you, `git revert` is a true rollback.

Verified end to end — the same digest in all three environments:

```
dev      ghcr.io/…/api-service@sha256:37cb794a…b4f3b
staging  ghcr.io/…/api-service@sha256:37cb794a…b4f3b
prod     ghcr.io/…/api-service@sha256:37cb794a…b4f3b
```

---

## 4. Vault and secrets

**Before:** Vault in dev mode — in-memory, auto-unsealed, root token — and a
`postgres-credentials` Secret created by hand on each cluster.

**Now**, and running in all three environments:

**Persistent and self-initialising.** Vault runs standalone on **file storage**
backed by a PVC, so secrets survive a restart. A `vault-config` CronJob
initialises *and unseals* it, and configures the auth roles and policies. Nobody
ran `vault operator init`. It reconciles every 2 minutes and is idempotent, so a
run that lands before Vault is reachable simply fails and the next one succeeds —
recovery needs no operator.

**The secret store holds no secret.** The ESO `ClusterSecretStore` contains no
token, password or key. External Secrets presents its own **ServiceAccount JWT**,
Vault verifies it against the cluster API via Kubernetes auth, and the bound
policy is read-only and scoped to a single path.

**Long-lived environments use dynamic database credentials.** Vault mints a brand
new PostgreSQL role per application from its database secrets engine, with a 1h
TTL, and revokes it on expiry. There is no shared database password — so the
rotation ordering problem simply does not exist: there is never a moment when the
application and the database disagree about a shared secret.

**Each application authenticates as itself.** Not through the ESO controller's
identity — through its own ServiceAccount, with a policy allowing only
`database/creds/<its-own-name>`. `api-service` cannot obtain `inventory-service`'s
database access even if it tried.

**Previews are the deliberate exception.** A preview runs its own throwaway
Postgres that Vault has no connection to, so previews keep the static KV
credential. Same mechanism, different credential source.

Net effect: the database password no human has ever seen, it exists nowhere in
git, and the application still reports `{"db":"ok"}`.

---

## 5. Set it up and test it end to end

Budget ~45–60 minutes, mostly waiting on VMs.

### Prerequisites

```bash
brew install colima kubectl argocd docker gh jq
```

Four VMs at 2 CPU — **~14 GiB RAM free**, ~80 GiB disk. macOS / Apple Silicon
(the VM-to-VM networking fix is colima/lima-specific — see
[`docs/spike-01-vm-networking.md`](docs/spike-01-vm-networking.md)). On 16 GiB,
run one spoke instead of three.

### GitHub, once

```bash
gh repo fork <upstream>/infra-interview --clone && cd infra-interview
./scripts/set-owner.sh <your-account>     # stamps the OWNER placeholder
git commit -am "chore: stamp owner" && git push
```

Then, in the fork:

1. **Settings → Actions → General → Workflow permissions** → tick **"Allow GitHub
   Actions to create and approve pull requests."** Without it the prod promotion
   dies at `gh pr create` *after* it has already retagged.
2. Push any commit to `main` so CI builds the first images.
3. **Make both GHCR packages public** (`api-service`, `inventory-service` →
   Package settings → Change visibility). There is **no `imagePullSecret` in this
   repo**, and packages default to private — miss this and every pod sits in
   `ImagePullBackOff` with no useful signal.
4. **Protect `main`** — require a PR and status checks. Prod auto-syncs, so
   merging deploys; without protection any push to `main` reaches production.

### Locally

```bash
export GH_OWNER=<your-account>
export GH_PAT=<token with Contents: read+write>   # Image Updater's git write-back

./scripts/setup.sh          # hub + 3 spokes, Argo CD, Image Updater, registration
SPOKES="dev" ./scripts/setup.sh   # lighter: one spoke
```

It pauses once for an Argo CD login:

```bash
kubectl --context colima-hub -n argocd port-forward svc/argocd-server 8090:443 &
kubectl --context colima-hub -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d
argocd login localhost:8090 --username admin --insecure
```

`ImagePullBackOff` at first is expected — the overlays name tags from before you
forked. Image Updater polls every 2 minutes, finds what CI built, and commits it
to your repo as `build: automatic update of api-service-dev`.

### The loop

```bash
# 1. change + preview environment
git checkout -b feat/try-it && git commit -am "feat: try it" && git push -u origin feat/try-it
gh pr create --fill && gh pr edit --add-label preview     # full stack in its own namespace

# 2. merge — CI builds, Image Updater rolls it into dev. You deploy nothing.
gh pr merge --squash

# 3. promote to staging — leave source_tag EMPTY
gh workflow run promote.yml -f service=api-service -f target_env=staging

# 4. promote to prod — opens a PR; merging it deploys
gh workflow run promote.yml -f service=api-service -f target_env=prod
gh pr merge <n> --squash

# 5. rollback, also without naming a tag
gh workflow run rollback.yml -f service=api-service -f environment=dev
```

**Prove it was the same artifact the whole way:**

```bash
for env in dev staging prod; do
  printf '%-8s ' $env
  kubectl --context colima-$env -n shop get pods -l app.kubernetes.io/name=api-service \
    -o jsonpath='{.items[0].status.containerStatuses[0].imageID}{"\n"}'
done
```

**And that config promoted correctly:**

```bash
kubectl --context colima-prod -n shop exec deploy/api-service -- \
  wget -qO- localhost:8080/orders/summary
# {"environment":"prod","order_limit":100,"orders":0,"version":"638e6b6"}
```

`order_limit` travelled with the image (category 4). `environment` did not
(category 3) — and neither did the sandbox payments URL.

Teardown: `./scripts/cleanup.sh`

---

## 6. Known limits

- **`inventory-service` in prod is `ImagePullBackOff`** — never promoted, so its
  overlay holds a placeholder tag. That is the gate working.
- **`api-service` in prod runs 1 replica.** `maxSurge=1 / maxUnavailable=0` needs
  an extra pod before retiring an old one, and a 2-CPU node had no room — the
  first promotion deadlocked. Only ever visible on the *first* update.
- **Vault is per-cluster, not central**, and the unseal key lives in a Kubernetes
  Secret — the direct cost of file storage over dev mode. Production means raft
  plus KMS auto-unseal.
- **Branch protection is assumed, not demonstrated.** Prod auto-syncs, so it
  matters.
- **`make status` / `test` / `deploy` and `TROUBLESHOOTING.md`** are from the
  original exercise and reference the old single-cluster Helm setup.
