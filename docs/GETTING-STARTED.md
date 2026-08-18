# Getting started — run the whole thing locally

This sets up four Kubernetes clusters on your laptop and walks an application
change from a pull request all the way into production, the way the pipeline
actually does it.

Budget **45–60 minutes**, most of it waiting on VMs.

---

## What this is

The exercise shipped a `setup.sh` that built two images and ran four
`helm upgrade --install` commands against one cluster. That is a deployment
script, not a deployment *system*: nothing recorded what should be running, so
nothing could detect drift, promote a version, or roll one back.

This replaces it with a GitOps pipeline across four clusters — a hub running
Argo CD, and dev / staging / prod spokes.

**Clusters provision themselves.** You register a cluster with an `env` label and
ApplicationSets do the rest. There is no per-environment list of what to install;
the environment is read out of the repo path (`gitops/apps/<app>/envs/<env>`).

**Config is split by how it behaves under promotion**, not by what it looks like.
Four categories, each with one physical home: the image tag and the business
settings validated with it are promoted; Kubernetes settings and
environment-identity settings never are. It is structural — a promotion
physically cannot reach the sandbox payment URL, because no file under `envs/`
contains it.

**Promotion never rebuilds.** It retags an existing digest and asserts the digest
is unchanged, so the bytes that passed staging are the bytes production runs.
Proven live: dev, staging and prod all report the same
`sha256:37cb794a…`.

**Nothing is typed into a form.** Argo CD Image Updater writes the deployed tag
back into git, so the overlay *is* the record of what an environment runs.
Promotion reads it; rollback reads the same file's history. You pick a service
and a target and press Run.

**Production differs in exactly one way**: it is invisible to Image Updater, so
nothing can move its tag except a reviewed pull request. It auto-syncs like every
other environment — the merge is the deploy.

Full reasoning is in [`SOLUTION.md`](../SOLUTION.md); the config model is in
[`gitops/README.md`](../gitops/README.md); raw evidence is in
[`verification-log.md`](verification-log.md).

---

## Prerequisites

**Hardware.** Four VMs at 2 CPU each. Budget **~14 GiB RAM free** and ~80 GiB
disk. On a 16 GiB machine, bring up only `dev` (see below) — the full ladder
needs more.

Built and tested on **macOS / Apple Silicon**. Images are `linux/amd64` +
`linux/arm64`, but the VM-to-VM networking fix is colima/lima-specific — see
[`spike-01-vm-networking.md`](spike-01-vm-networking.md).

**Tools.**

```bash
brew install colima kubectl argocd docker gh jq
```

`python3` is used by the scripts and workflows; macOS ships one.

**Accounts.** A GitHub account you can fork into. Everything else is local.

---

## Part 1 — GitHub setup (once)

### 1. Fork and stamp your account

The manifests carry a literal `OWNER` placeholder in ApplicationSet repo URLs and
in every overlay's image reference. Argo CD reads those straight from git, so
they cannot be substituted at apply time.

```bash
gh repo fork <upstream>/infra-interview --clone
cd infra-interview
./scripts/set-owner.sh <your-github-account>
git commit -am "chore: stamp owner"
git push
```

It prints every file it touched and then re-greps to prove no placeholder
survived.

### 2. Create a token

Image Updater needs to **write** to your fork. The repo is public, so Argo CD
reads it anonymously; the token exists only for the write-back.

Create a PAT with **Contents: read and write** on the fork, then:

```bash
export GH_OWNER=<your-github-account>
export GH_PAT=<the-token>
```

### 3. Two repo settings that will bite you

**Settings → Actions → General → Workflow permissions**
→ tick **"Allow GitHub Actions to create and approve pull requests."**

Without it the prod promotion fails at `gh pr create` — *after* it has already
retagged the image, leaving the registry ahead of git.

**Optional but recommended.** Promotion PRs are authored by
`github-actions[bot]`, which the default contributor-approval policy treats as a
first-time contributor, so their checks sit waiting on "Approve and run":

```bash
gh api --method PUT repos/$GH_OWNER/infra-interview/actions/permissions/fork-pr-contributor-approval \
  -f approval_policy=first_time_contributors_new_to_github
```

### 4. Build the first images

A fresh fork has an empty registry, and the overlays reference tags built under
*your* account that do not exist yet. Push any commit to `main` to trigger CI:

```bash
git commit --allow-empty -m "ci: first build" && git push
gh run watch
```

### 5. Make the packages public — do not skip this

There is **no `imagePullSecret` anywhere in the repo**. Packages created by
Actions default to *private*, so every pod would sit in `ImagePullBackOff`
forever with no obvious cause.

For **each** of `api-service` and `inventory-service`: your profile → Packages →
the package → Package settings → Change visibility → **Public**.

Verify anonymously:

```bash
TOKEN=$(curl -s "https://ghcr.io/token?scope=repository:$GH_OWNER/api-service:pull&service=ghcr.io" \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOKEN" \
  https://ghcr.io/v2/$GH_OWNER/api-service/tags/list
```

`200` means public. `401`/`403` means still private.

### 6. Protect `main` — required, because prod auto-syncs

Merging a promotion PR now deploys to production with no second step. That is
only safe if the merge is a real gate.

Settings → Rules → New ruleset, targeting `main`: **require a pull request**,
**require status checks** (`validate`, `detect changes`), and **block direct
pushes**. Add required approvals if more than one person will use the repo.

Skip this and any push to `main` deploys straight to prod.

---

## Part 2 — Local clusters

```bash
export GH_OWNER=<your-github-account>
export GH_PAT=<the-token>

./scripts/setup.sh
```

That brings up the hub and all three spokes, installs Argo CD and Image Updater,
plants the root Application, and registers each spoke.

**Lighter run** — one spoke instead of three:

```bash
SPOKES="dev" ./scripts/setup.sh
```

**Tune VM size** with `CPUS=` / `MEMORY=` (defaults 2 CPU / 4 GiB).

Midway it pauses and asks you to log in to Argo CD:

```bash
kubectl --context colima-hub -n argocd port-forward svc/argocd-server 8090:443 &
argocd login localhost:8090 --username admin --insecure
```

Admin password:

```bash
kubectl --context colima-hub -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d
```

Then press enter. (`register-spoke.sh` itself only uses `kubectl`; the login is
for the `argocd` convenience commands afterwards.)

### Running the steps individually

```bash
./scripts/cluster-up.sh hub          # empty cluster, installs nothing
./scripts/bootstrap-argocd.sh        # the one imperative step in the system
./scripts/register-spoke.sh dev      # after this, dev provisions ITSELF
```

`register-spoke.sh` is the interesting one. It writes the Argo CD cluster Secret
directly rather than using `argocd cluster add`, because that command makes the
Argo CD *server* dial the kubeconfig URL — unreachable from inside the hub VM —
and aborts *after* creating the ServiceAccount on the spoke, leaving a half-done
state. The `env` label on that Secret is the entire contract between "a cluster
exists" and "it provisions itself".

---

## Part 3 — Check it came up

```bash
kubectl --context colima-hub -n argocd get applications
```

Expect `Synced` / `Healthy` across the board within a few minutes.

**`ImagePullBackOff` at first is normal.** The overlays still name tags from
before you forked. Image Updater polls every 2 minutes, finds the tag CI just
built, and commits it to your repo:

```bash
kubectl --context colima-hub -n argocd logs deploy/argocd-image-updater-controller \
  --tail=20 | grep 'Processing results'
```

(Note the `-controller` suffix — v1.x uses a kubebuilder layout and the
Deployment is not called `argocd-image-updater`.)

`applications=N images_updated=…`. Once it writes back, pull `main` and you will
see `build: automatic update of api-service-dev` — a commit the robot made.

**`applications` should never count prod.** With dev and staging up it is `4`
(two services × two environments). If prod appears there, the safety boundary has
broken and `gitops-validate` should have caught it.

---

## Part 4 — The end-to-end test

### Stop 1 — a change, and a preview environment

```bash
git checkout -b feat/try-it
# edit something in apps/api-service/
git commit -am "feat: try it" && git push -u origin feat/try-it
gh pr create --fill
gh pr edit --add-label preview
```

The `preview` label is what the PR-preview ApplicationSet selects on. It builds
both services at `pr-<sha>` and stands up a whole namespaced stack — its own
postgres included — then tears it down when the PR closes.

### Stop 2 — merge, and dev deploys itself

```bash
gh pr merge --squash
```

CI builds `dev-<timestamp>-<sha>`. Image Updater notices, writes it into
`gitops/apps/api-service/envs/dev/kustomization.yaml`, Argo CD syncs. You did not
deploy anything.

### Stop 3 — promote to staging

```bash
gh workflow run promote.yml -f service=api-service -f target_env=staging
```

**Leave `source_tag` empty.** The plan job resolves what dev is running, straight
out of git:

```
resolved api-service: dev is running dev-20260817T220707Z-638e6b6
```

It retags that digest as `staging-…`, asserts the digest did not change, and
stops. Image Updater does the rest.

### Stop 4 — promote to prod

```bash
gh workflow run promote.yml -f service=api-service -f target_env=prod
```

This one opens a pull request instead of deploying, because prod is invisible to
Image Updater. The diff is one line.

> If you run this within a minute or two of stop 3, expect a warning that staging
> has not converged — the retag moved the registry but Image Updater has not yet
> moved the overlay, so resolution would pick up the *previous* tag. Wait for the
> write-back commit, or pass an explicit `source_tag`.

Review it, then:

```bash
gh pr merge <n> --squash
```

That deploys. Prod auto-syncs, so there is no separate Sync step.

### Stop 5 — prove it is the same artifact

The claim the whole pipeline rests on:

```bash
for env in dev staging prod; do
  printf '%-8s ' $env
  kubectl --context colima-$env -n shop get pods -l app.kubernetes.io/name=api-service \
    -o jsonpath='{.items[0].status.containerStatuses[0].imageID}{"\n"}'
done
```

Three identical `sha256:` digests. No rebuild happened at any point.

And the config model, observable from inside the pod:

```bash
kubectl --context colima-prod -n shop exec deploy/api-service -- \
  wget -qO- localhost:8080/orders/summary
```

```json
{"environment":"prod","order_limit":100,"version":"638e6b6"}
```

`order_limit` was promoted with the image (category 4). `environment` was not
(category 3) — and neither was the sandbox payments URL.

### Rollback

```bash
gh workflow run rollback.yml -f service=api-service -f environment=dev
```

Blank `good_tag` returns to the tag dev ran *before* the current one, read from
the overlay's git history. The run summary lists the last ten real candidates.

Note it rolls **forward**: it republishes the known-good digest under a newer tag.
A `git revert` would not work on dev or staging — Image Updater would find the
bad tag still newest and write it straight back.

---

## Teardown

```bash
./scripts/cleanup.sh              # all four profiles
./scripts/cleanup.sh dev          # or just one
```

---

## Things that will look broken and are not

**`inventory-service` in prod is `ImagePullBackOff`.** It has never been promoted,
so its overlay holds `prod-00000000T000000Z-0000000`. That is the gate working.
Promote it if you want it running.

**`api-service` in prod runs 1 replica.** The rollout strategy is
`maxSurge=1 / maxUnavailable=0`, so an update must schedule an extra pod before
retiring an old one. At 3 replicas on a 2-CPU node there was no room, and the
first promotion deadlocked with the Deployment on the new image and every serving
pod on the old one. It only appears on the *first* update, because the initial
create has no surge to perform.

**`postgres-prod` shows `OutOfSync`.** Pre-existing and unrelated.

**Vault is a toy.** Dev mode, in-memory, auto-unsealed — every restart destroys
all secrets. Fine for demonstrating the ExternalSecrets path, not a source of
truth.

**External Secrets' two largest CRDs** occasionally need applying by hand with
`--server-side` on a fresh cluster.

**Port-forwards die** when the shell that owns them exits.

**`make status` / `make test` / `make deploy` are from the original exercise** and
reference the `interview-test` namespace that no longer exists. `make setup` and
`make clean` still work — they call the scripts above. `TROUBLESHOOTING.md` is
likewise the original and describes the single-cluster Helm setup.
