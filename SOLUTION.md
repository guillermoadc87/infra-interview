# Solution Documentation

**Candidate Name**: Guillermo Diaz
**Chosen Path**: Path 4 — CI/CD & GitOps
**Time Spent**: ~5 hours

## Summary

The deployment process was a shell script running `docker build` and four
`helm upgrade --install` commands against one cluster. I replaced it with a
pull-based GitOps system: clusters come up **empty**, and a cluster provisions
itself the moment it is registered to Argo CD **with an `env` label**. Helm charts
were migrated to Kustomize with the four categories of environment configuration
placed in physically distinct homes, so that what promotes between environments
and what must never promote is a property of the directory layout rather than of
someone's discipline.

The single most useful thing in here is not the tooling choice — it is that the
repository now makes a whole class of mistake structurally impossible: there is
no file you can copy that would move the sandbox payment endpoint into
production.

> **Start here:** [`OVERVIEW.md`](OVERVIEW.md) — the three-level Argo CD layout,
> the config model, promotion, and the Vault work, in about five minutes; then a
> section that sets all four clusters up locally and walks a change from pull
> request to production. This document is the long form underneath it.

---

## Problems with the current deployment process

Findings from reading `scripts/setup.sh`, `Makefile`, `charts/`, and `terraform/`:

1. **Deployment is a human running a script.** No audit trail of who deployed
   what, when, or to where. The cluster's state exists only in the cluster.
2. **No CI at all.** No `.github/`, no tests (**zero** `*_test.go` files existed),
   no build automation, no image scanning.
3. **No registry.** `make build` produced `:latest` locally and relied on k3s
   sharing the Docker daemon. That works for exactly one machine and cannot
   deliver an image to a second cluster.
4. **`:latest` with `pullPolicy: Never`.** No immutability: a rebuild silently
   changes what "latest" means, with no rollout trigger and no way to say which
   code is running.
5. **No health validation.** Neither service exposed a health endpoint, so there
   was nothing for a probe to asssert on and no definition of "the deploy worked".
6. **No rollback.** Helm keeps release history, but nothing recorded intent, so
   rollback meant a human re-running a command with older inputs.
7. **Ordering was implicit and fragile.** `setup.sh` worked because it ran four
   commands in a fixed sequence; both services called `log.Fatal` if the database
   was not up, so start order mattered and a database blip meant CrashLoopBackOff.
8. **Secrets in plaintext in git** (`DB_PASSWORD: "postgres"` in `values.yaml`)
   while a secrets manager sat unused in the same namespace.
9. **Duplicate ownership of the same fact.** `terraform/main.tf` creates the
   `orders` and `inventory` databases via the `postgresql` provider, and the
   postgres chart's init ConfigMap creates the same two databases. Two systems
   own one piece of state.
10. **Errors leaked to clients.** Both services passed `err.Error()` straight into
    HTTP responses, exposing schema and connection detail.
11. **No resource limits, no probes, no securityContext, containers as root.**

---

## Changes Made

### Change 1: Clusters come up empty; git is the only way in
**What**: Split `setup.sh` into `cluster-up.sh` (a bare k3s node, nothing else),
`bootstrap-argocd.sh` (Argo CD + Image Updater + the root app), and
`register-spoke.sh`. Deleted the Helm installs and the `make build`/`make deploy`
targets.
**Why**: The old script conflated "make me a cluster" with "install four things
onto it". Once workloads arrive from git, keeping an imperative path alongside it
is actively harmful — someone will use it, and `helm install` over a
server-side-applied Argo CD release produces field-manager conflicts that look
like permission errors.
**How**: Four colima profiles — `hub` running Argo CD, and `dev`/`staging`/`prod`
as spokes. Bootstrap remains imperative and always will: something has to install
the thing that installs everything else.

### Change 2: Helm → Kustomize, with the config taxonomy made physical
**What**: `gitops/apps/<app>/{base,variants,envs}` and
`gitops/platform/{postgres,vault}`.
**Why**: The charts had no per-environment story at all — one `values.yaml` each.
The interesting problem is not templating, it is deciding which settings promote
between environments and which must not. Full table in `gitops/README.md`.
**How**: version → `images[].newTag`; Kubernetes settings → `replicas.yaml` /
base; mostly-static business settings → Kustomize **Components** under
`variants/`; non-static business settings → `envs/<env>/settings.yaml`. Promotion
touches only the last two, so the first two cannot be promoted by accident.

### Change 3: Three-level Argo CD structure driven by cluster labels
**What**: `root/root-application.yaml` → `appsets/*.yaml` → overlays.
**Why**: The requirement was that registering a cluster provisions it. A cluster
generator selecting on `env` makes the label the entire contract.
**How**: Each ApplicationSet is a matrix of a git generator and a cluster
generator, with the env label value read out of the repo path
(`{{index .path.segments 4}}`) so one ApplicationSet spans all environments.

### Change 4: Health, readiness, and a retry loop
**What**: `/healthz` (liveness, **never** touches the database), `/readyz`
(readiness, does), and a background bounded-retry database connect replacing
`log.Fatal`. Plus unit tests, input validation, and no more leaking driver errors.
**Why**: "Deployment safety: health validation" needs something to assert on. The
liveness/readiness split matters more than it looks: a liveness probe that checks
the database turns a database blip into a fleet-wide restart, converting a
dependency problem into an outage. The retry loop is what makes pod start order
stop mattering.
**How**: `dbReady` atomic flag plus a live ping in `/readyz`; the HTTP listener
starts immediately so a pod that cannot reach the database is *unready*, not dead.

### Change 5: CI, and the loop it must not create
**What**: `ci.yml` (gofmt → vet → race tests + coverage → multi-arch build → GHCR),
`gitops-validate.yml`, `promote.yml`.
**Why**: `linux/arm64` is mandatory — the clusters are aarch64, and an amd64-only
image fails with `exec format error` on every pod.
**How**: Cross-compilation via `BUILDPLATFORM` rather than QEMU. Critically,
`ci.yml` carries `paths-ignore: gitops/**`: without it, Image Updater's write-back
commit triggers a build, which pushes an image, which triggers Image Updater — a
slow infinite loop that costs money.

### Change 6: Ordering that is honest about what it does
**What**: A `wait-for-postgres` PreSync hook Job in each app's `base/`.
**Why**: Sync waves order resources *within* one Application. They do **not**
order Applications generated by different ApplicationSets, and an ApplicationSet
reports Healthy once it has *generated* its Applications — not once those are
healthy. Claiming wave-based cross-app ordering would have been wrong.
**How**: The hook holds the sync open until `pg_isready` succeeds, then fails
cleanly on a 300s deadline and retries under the Application's backoff. Combined
with Change 4, ordering stops being load-bearing.

---

## Design Decisions & Trade-offs

### Decision 1: Where the container tag lives
**Options**: (A) `envs/<env>/version.yaml` strategic-merge patch, as in the
well-known reference model. (B) the `images:` transformer in the overlay's
`kustomization.yaml`.
**Chosen**: B. Image Updater's `write-back-target: kustomization` writes
`images[].newTag`. With both present, Kustomize's ImageTagTransformer runs *after*
patches, so `images:` wins silently and `version.yaml` becomes a file that reads
as authoritative and does nothing.
**Trade-off**: Category 1 no longer promotes by `cp`, which is the reference
model's nicest property. Paid for by making promotion a workflow that writes the
same field the robot writes, so there is exactly one writer. CI fails the build if
any file under `envs/` or `variants/` names an image.

### Decision 2: Production differs in exactly one way — who may move the tag
**Options**: (A) all environments identical. (B) prod manual-sync *and* invisible
to Image Updater. (C) prod auto-synced like everything else, but invisible to
Image Updater.

**Chosen**: C, via `templatePatch` gated on the env path segment. Prod receives
neither the `image-updater: enabled` label nor the annotations, so the controller
cannot see it even if misconfigured. Sync policy is now identical everywhere.

I started at B and moved to C, because the second gate turned out to be one gate
counted twice. Prod's overlay can only change through a reviewed promotion PR, so
whoever could press Sync could already merge; requiring both did not add an
approval, it just added a step — and a window in which git and the cluster
disagreed. The honest gate is the merge. Auto-sync makes that literal: for
production, the merge button *is* the deploy button.

**Trade-off, and it is a real one**: the Image Updater exclusion used to have
manual sync behind it as a backstop. If the exclusion ever broke, a stray tag
write would sit in git until a human chose to apply it. Now it would deploy
itself. One boundary instead of two, so that boundary has to be load-bearing —
`gitops-validate` therefore checks it *structurally*, asserting every
`image-updater` reference sits inside the `ne $env "prod"` branch. A plain grep
would still pass if an annotation were moved after the `{{- else }}`; the check
is tested against exactly that mutation.

Prod also gains `selfHeal`, which it previously lacked — drift is now corrected
rather than merely reported. The cost is that break-glass `kubectl` edits are
reverted within seconds, which is right on a normal day and can be hostile
mid-incident. The escape hatch is to suspend automated sync on the Application.

Rollback is unaffected. Decision 3's "on prod `git revert` is a true rollback"
rests on the Image Updater exclusion, not the sync policy — nothing is racing you
to write the tag back. Auto-sync only makes the revert apply faster.

### Decision 3: Rollback is forward, not backward
`git revert` **does not roll back** dev or staging. The rule is "the newest allowed
tag wins", so reverting the tag bump just means Image Updater re-reads the
registry, finds the bad tag still newest, and writes it back. The revert is
undone automatically, and the history reads as the tool fighting you.
**Chosen**: roll the registry *forward* — re-promote a known-good digest under a
new, higher-sorting tag. Same mechanism as promotion, forward-only audit trail.
On prod this problem does not exist, because nothing is racing you.
**Trade-off**: rollback requires a registry operation rather than a git operation,
which is unintuitive until you have been bitten by the alternative.

### Decision 4: Promotion retags a digest; it never rebuilds
A rebuild from the same commit is not the same artifact — base tags move, module
proxies can drift. "It passed staging" only means something if prod runs the same
digest, and the vulnerability scan only carries forward if the bytes do. The
workflow asserts the digest is unchanged and fails loudly if not.

### Decision 4a: Promotion resolves the tag from git rather than asking for it
**Options**: (A) the operator supplies the exact source tag. (B) the workflow
reads what the source environment runs.

**Chosen**: B. `source_tag` is optional; blank means "promote what the source
environment is running", read from `envs/<env>/kustomization.yaml` via
`scripts/image-tag.sh`. The original form required a developer to locate a string
like `dev-20260817T220707Z-638e6b6` in GHCR and paste it in — data entry for a
value the repository already held, since Image Updater's whole job is writing that
tag back into git.

The interesting part is that this *tightens* the guarantee. The old validation was
a shape regex, so any well-formed tag passed, including one built from an
unrelated commit. Resolution makes "the artifact that was promoted" and "the
artifact the source environment converged on" the same statement by construction.
`scripts/image-tag.sh` is also the one component that writes the tag, so read and
write cannot disagree about where it lives — which is what the "exactly one
writer" claim in `gitops/README.md` depends on.

**Trade-off**: promotion now depends on Image Updater having converged. Retagging
staging does not itself move the staging overlay, so promoting to prod inside that
window resolves the *previous* tag. Rather than block on it, the workflow compares
the overlay against GHCR and warns when a newer `staging-` tag exists; resolving
what staging has converged on stays correct, it just must not be silent.

Rollback resolves the same way, one step further back, from the git history of the
same file. History is the better source: git records what was **deployed**, the
registry records what was **built**.

### Decision 5: Two variant axes rather than one
`variants/tier/` (prod vs non-prod) and `variants/env/` (which environment).
A two-valued tier cannot express a three-valued fact like `ENVIRONMENT`, and
putting environment identity under `envs/` would place a never-promoted value in
the directory promotion copies *from*.
**Trade-off**: `variants/` is duplicated per application. At ~8 lines each that is
cheaper than a shared directory reached by `../../../../`, but it should be
consolidated at roughly five services.

### Decision 6: Postgres stays ephemeral
It is a `Deployment` with an `emptyDir`, unchanged from the original. Adding a PVC
would make the init script a one-shot that then silently stops applying, and an
RWO volume under a rolling Deployment deadlocks on upgrade. Storage is Path 1's
subject, and the GitOps structure is identical whether this is an emptyDir, a
StatefulSet, or RDS.

---

## Verification Steps

Full transcript in `docs/verification-log.md`; the networking investigation is in
`docs/spike-01-vm-networking.md`.

```bash
# 1. Empty clusters
./scripts/cluster-up.sh hub
./scripts/cluster-up.sh dev

# 2. Argo CD on the hub, then the root app
GH_OWNER=<owner> GH_PAT=<token> ./scripts/bootstrap-argocd.sh

# 3. THE CLAIM: register one cluster with a label, run nothing else
./scripts/register-spoke.sh dev
argocd app list
```

### What I actually ran and watched work

- **Hub reaches spoke with full TLS verification.** `verified_http=401` using the
  real k3s CA and hostname checking.
- **A cluster labelled `env=dev` provisioned itself.** After registering one
  spoke and applying the root app, exactly three Applications appeared —
  `api-service-dev`, `postgres-dev`, `vault-dev` — all `env=dev`, all aimed at the
  spoke. **No staging or prod Applications exist, because no staging or prod
  cluster is registered.** That is the claim, and it is the whole design.
- **The dependency gate gated.** `api-service` sat `OutOfSync/Missing` with
  `wait-for-postgres` Running while postgres was in `CreateContainerConfigError`
  (no credentials Secret — deliberately out of git). Creating the Secret let
  postgres go Healthy and **the gate released on its own**.
- **Vault installed from the upstream chart** via a multi-source Application
  (`Synced`/`Healthy`).
- **Argo CD syncing from GitHub**: `Synced to main (08f2fbf)`, `Healthy`.
- **All Kustomize overlays build**, and all four config categories render into the
  right places.
- **Go tests, `go vet`, `gofmt` pass locally** for both services.
- **The CI manifest gate caught two real defects in my own work** before they
  shipped: that Kustomize Components are not standalone-buildable, and that my
  "no image outside base" rule was written too broadly to allow the PreSync hook
  Job's postgres image.

### The full loop, verified end to end

This is the claim the path asks for, and it ran:

| step | evidence |
|---|---|
| commit | `744a5be` pushed to `main` |
| CI | run `31987247191` **success** — `detect changes`, `build api-service`, `build inventory-service` |
| registry | `ghcr.io/guillermoadc87/api-service:dev-20260817T021244Z-744a5be`, and `linux/amd64` **+ `linux/arm64`** confirmed via the registry API |
| Image Updater | `images_updated=2 errors=0`, then `git push origin main` |
| git | commits `c95bdd7` / `30be1d4` authored by `argocd-image-updater <noreply@argoproj.io>`, moving `images[].newTag` in the dev overlays |
| Argo CD | synced to `30be1d4` |
| runtime | `api-service` and `inventory-service` both `1/1 Running` |

And the running code is provably the code CI built:

```
$ curl localhost:18080/healthz
{"status":"ok","version":"744a5be"}          # == the commit CI built

$ curl localhost:18080/readyz
{"db":"ok","status":"ok","version":"744a5be"}

$ curl -X POST localhost:18080/orders -d '{"product_id":1,"quantity":2,"customer_id":"verify-e2e"}'
$ curl localhost:18080/orders/1
{"id":1,"product_id":1,"quantity":2,"customer_id":"verify-e2e","status":"pending",...}

# category 4 is genuinely enforced: dev's FEATURE_ORDER_LIMIT is 100
$ curl -X POST localhost:18080/orders -d '{"product_id":1,"quantity":500,...}'
http=400
```

**The CI feedback-loop guard also proved itself.** Image Updater's two write-back
commits triggered `gitops-validate` (correct — `gitops/**` changed) and **did not
trigger `ci`**. Without `paths-ignore: gitops/**` that commit would have started a
build, which would have pushed an image, which would have triggered Image Updater
again.

At no point did I run `kubectl apply`, `helm`, or `docker build` to deploy
anything. The only imperative actions were bootstrapping Argo CD and registering
the cluster.

### What I did NOT verify — stated plainly

- **staging and prod clusters were never created.** Only `hub` and `dev` VMs ran.
  Their manifests exist, render, and are CI-validated, but the label mechanism has
  been demonstrated across one environment rather than three.
- **The promotion workflow has not been executed**, so `prod-*`/`staging-*` retagging
  and the GitHub Environment reviewer gate are authored and unrun.
- **The rollback procedure has not been executed.** The reasoning about `git revert`
  being undone by Image Updater follows from the mechanism ("newest allowed tag
  wins") and from watching that mechanism work, but I did not stage a rollback.
- **Early Argo CD verification used a locally-served git repo**, because the fork
  did not exist yet — the repo was served to the VMs over `git://` on a throwaway
  branch. Same root app, same ApplicationSets, same overlays; only the URL
  differed. Everything in the table above is from the real GitHub path.

### A bug this process found in my own work

`postgres-credentials` was created only in the `platform` namespace, but the
applications run in `shop`, and Secrets are namespace-scoped — so
`inventory-service` failed with `CreateContainerConfigError: secret
"postgres-credentials" not found`. The credential is needed in both namespaces.
Fixed in `gitops/bootstrap/spoke/postgres-credentials.example.yaml`, and it is a
concrete argument for External Secrets: the right fix is one backend key synced
into each namespace, not a human copying a Secret twice.

### A second one, found by removing a manual step

Making promotion resolve its own tag exposed a latent defect in the version it
replaced. `source_tag` was a single string, but the promotion job fans out over a
service matrix — so `service: all` sent one tag to every leg. The services are
built independently, so their tags genuinely differ:

| service | staging |
|---|---|
| `api-service` | `staging-20260817T222457Z-638e6b6` |
| `inventory-service` | `staging-20260817T143153Z-15bdb10` |

Promoting `all` to prod would have looked up
`inventory-service:staging-20260817T222457Z-638e6b6`, a tag that does not exist.
It stayed hidden because CI originally rebuilt both services on every merge, which
kept their tags identical by accident; commit `55be4d5` ("build only services that
changed") made divergence the norm and turned a latent bug into a live one.

Per-service resolution removes it structurally rather than patching it — each leg
resolves its own tag — and supplying an explicit tag is now rejected when combined
with `all`, because one tag names one artifact and cannot mean two things.

Worth recording because the manual step was hiding it: a human pasting a tag they
had just looked up for *one* service would rarely notice they had implicitly
claimed it applied to both.

---

## Production Considerations

- **Secrets.** `postgres-credentials` is created out of band per cluster, and that
  is the one thing not in git. A real deployment needs External Secrets, Sealed
  Secrets, or SOPS — the Vault already in the platform layer is the obvious
  backend. Not implemented; the gap is explicit in
  `gitops/bootstrap/spoke/postgres-credentials.example.yaml`.
- **Vault here is a toy.** Dev mode is in-memory and auto-unsealed, so every pod
  restart destroys all secrets. It can never be a source of truth as configured.
  `prod` inherits dev mode from `base/values.yaml`; real production means
  `ha.enabled` + raft + auto-unseal.
- **Argo CD's own credentials.** The Image Updater PAT has repo write access. It
  should be a deploy key or GitHub App scoped to one repository, rotated, and
  separate from the registry credential — two credentials, two blast radii.
- **`argocd-manager` gets cluster-admin** on each spoke, which is what
  `argocd cluster add` does by default. A real fleet scopes this per project.
- **No progressive delivery.** A failed PostSync hook makes the sync loudly red;
  it does not roll back. Automatic rollback needs Argo Rollouts with an analysis
  template. `maxUnavailable: 0` plus a readiness probe already means a bad image
  never takes traffic, which is the property that matters most.
- **Access patterns at scale** (the path's bonus question): the asymmetry here is
  the point. Dev optimises for speed (auto-update, auto-sync); prod optimises for
  auditability (no automated writer, PR-gated tag change, human-approved sync,
  GitHub Environment reviewers). The same pipeline, two control planes, chosen by
  risk tier rather than by convenience.
- **Colima networking is not production-representative.** The user-v2/vzNAT split
  is an artefact of local VMs; real clusters have routable API endpoints.

---

## Future Improvements

1. **Bring up staging and prod** to show one label selecting across three clusters
   and to exercise the promotion ladder end to end.
3. **Secrets via External Secrets + Vault**, removing the one out-of-band step and
   making cluster registration genuinely the only manual action.
4. **Argo Rollouts** for canary analysis and true automatic rollback.
5. **Migrate Image Updater off annotations.** v1 is CRD-driven; `useAnnotations`
   is the documented ApplicationSet-friendly path but is formally legacy.
6. **Resolve the duplicate database ownership** — delete the `postgresql_database`
   resources from `terraform/main.tf` now that the init script owns it, since the
   in-cluster path needs no tunnel and works on a fresh spoke.
7. **Postgres as a StatefulSet with a PVC**, or an external managed database.
8. **A promotion trigger that is not a form at all.** Tag resolution removed the
   typing, but `workflow_dispatch` still cannot populate a dropdown from data —
   which is why the override and rollback candidate lists are printed into the run
   summary instead of being selectable. A PR comment (`/promote staging`) or a
   merge-triggered promotion would remove the remaining navigation; both were
   deliberately left out here because promotion to staging should stay a human
   decision, and auto-promoting on every merge would make staging a copy of dev
   rather than a distinct gate.

---

## Questions & Assumptions

- **Assumed the exercise wanted a demonstrable mechanism over breadth.** I built
  one path deeply rather than touching all four. The `inventory-service` overlays
  exist but only `api-service` was materialised on a cluster.
- **Assumed "self-provisioning" meant label-driven cluster targeting**, hence the
  cluster generator rather than a simpler git-directory generator.
- **Assumed a public repo is acceptable** for an interview exercise; it makes
  Argo CD's read path credential-free. A private repo needs repo credentials and
  an imagePullSecret.
- **Question I would ask**: how many environments and clusters is this expected to
  scale to? Below ~5 services the per-app `variants/` duplication is fine; beyond
  that it should be shared, which changes the layout.
- **Question I would ask**: is prod expected to self-heal? I chose detected-but-
  not-corrected drift for prod, which is a real trade-off and could reasonably go
  the other way given that prod's git content is already PR-gated.

---

## AI Usage Disclosure

I used Claude (Anthropic) as a coding agent throughout, as the README invites. It
was used for: researching Argo CD ApplicationSet generator semantics against
upstream source and docs; drafting the Kustomize and workflow YAML; and writing
the scripts and this document.

Specific things I directed rather than accepted:

- The `argocd cluster add` failure and the vzNAT/user-v2 networking split were
  found by running the spike, not by asking a model — the first design assumed the
  kubeconfig address would work, and it does not.
- The `version.yaml` vs `images:` conflict was a genuine design collision between
  the reference model and Image Updater's write-back; I chose the deviation and
  had CI enforce it.
- Two proposed designs disagreed about whether Image Updater v1 supports
  annotations. I verified against the upstream docs rather than picking one —
  `useAnnotations: true` is real and is the documented ApplicationSet pattern.
- The claim that sync waves order Applications across ApplicationSets is a common
  and wrong assumption; I checked the ApplicationSet health assessment and
  replaced it with the PreSync hook.

Every command reported in the "what I actually ran" section above was executed and
its output observed. Nothing in the verification log is reconstructed.
