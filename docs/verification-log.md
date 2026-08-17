# Verification log

Raw evidence, captured as it was produced. Anything not shown here was not run.

## What has been verified end-to-end

### 1. Hub reaches spoke (see docs/spike-01-vm-networking.md)

```
$ colima ssh -p hub -- curl -s --cacert /tmp/dev-ca.crt \
    https://lima-colima-dev.internal:52660/version
verified_http=401
```

Full TLS chain and hostname verification. 401 is the pass condition: TCP+TLS
completed and the API answered; Argo CD supplies a bearer token separately.

### 2. A cluster registered with a label provisions itself

Only ONE spoke was registered:

```
$ ./scripts/register-spoke.sh dev
OK hub reaches the spoke API (HTTP 401)
NAME          TYPE     DATA   AGE   ENV   MANAGED-BY
cluster-dev   Opaque   3      0s    dev   gitops
```

The root Application was then applied. Nothing else was run — no per-app
command, no `kubectl apply` of any workload:

```
$ argocd app sync root
Message: successfully synced (all tasks run)

GROUP        KIND            NAME                STATUS  HEALTH   MESSAGE
argoproj.io  AppProject      platform            Synced           serverside-applied
argoproj.io  AppProject      apps                Synced           serverside-applied
argoproj.io  ApplicationSet  platform-helm       Synced  Healthy  All applications have been generated successfully
argoproj.io  ApplicationSet  platform-kustomize  Synced  Healthy  All applications have been generated successfully
argoproj.io  ApplicationSet  apps                Synced  Healthy  serverside-applied
```

```
$ kubectl -n argocd get applications
NAME              ENV      DEST                                     PATH
api-service-dev   dev      https://lima-colima-dev.internal:52660   gitops/apps/api-service/envs/dev
postgres-dev      dev      https://lima-colima-dev.internal:52660   gitops/platform/postgres/envs/dev
vault-dev         dev      https://lima-colima-dev.internal:52660   <none>
root              <none>   https://kubernetes.default.svc           gitops/appsets
```

Three things this proves at once:

- **The label is the whole contract.** Exactly three Applications appeared, all
  `env=dev`, all aimed at the dev spoke. No staging or prod Applications exist,
  because no staging or prod cluster is registered. Register one and they appear.
- **The path-segment indices are right.** `api-service` was extracted from
  segment 2 and `dev` from segment 4 of `gitops/apps/api-service/envs/dev`.
- **Both git generator modes work.** `apps` used a `directories` generator;
  `postgres` and `vault` used `files` generators discriminated purely by filename
  (`kustomize.json` vs `config.json`). `vault-dev` shows `PATH: <none>` because it
  is a multi-source Helm Application, not a path-rendered one.

### 3. The dependency gate actually gates

Before the database credentials existed:

```
NAME              SYNC        HEALTH
api-service-dev   OutOfSync   Missing        <- blocked
postgres-dev      Synced      Progressing
vault-dev         Synced      Healthy

$ kubectl -n shop get pods
wait-for-postgres-vlv4k   1/1   Running      <- PreSync hook holding the sync open
```

`postgres` was in `CreateContainerConfigError` because `postgres-credentials` is
deliberately not in git. api-service did not deploy against a database that was
not there; it waited, visibly, with a named Job whose logs say why.

After creating the Secret:

```
$ kubectl -n platform create secret generic postgres-credentials ...
$ kubectl -n platform rollout status deploy/postgres
deployment "postgres" successfully rolled out

NAME              SYNC     HEALTH
api-service-dev   Synced   Progressing   <- gate released, sync proceeded
postgres-dev      Synced   Healthy
vault-dev         Synced   Healthy
```

The gate released on its own. This is the mechanism that replaces the ordering
`setup.sh` used to get by running four helm commands in a fixed sequence.

### 4. Kustomize renders all four config categories

```
$ kubectl kustomize gitops/apps/api-service/envs/dev
  replicas: 1                                           # cat 2  envs/dev/replicas.yaml
  image: ghcr.io/OWNER/api-service:dev-...              # cat 1  envs/dev/kustomization.yaml
  - name: FEATURE_ORDER_LIMIT   value: "100"            # cat 4  envs/dev/settings.yaml
  - name: ENVIRONMENT           value: dev              # cat 3  variants/env/dev/
  - name: PAYMENTS_URL          value: ...sandbox...    # cat 3  variants/tier/non-prod/
  - name: LOG_LEVEL             value: debug            # cat 3  variants/tier/non-prod/
  - name: DB_HOST  value: postgres.platform.svc...      # env-invariant, base
```

## What has NOT been verified

Stated plainly, because the README asks for exactly this distinction.

- **The image half of the loop.** `api-service-dev` reached `Synced` and then the
  pod failed with `InvalidImageName` — the manifests still carry the literal
  `OWNER` placeholder, and no image has been built or pushed. CI → GHCR → Image
  Updater → git write-back → sync is **authored but not executed**. It requires
  a GitHub account to be authenticated and the fork pushed.
- **staging and prod spokes.** Only `hub` and `dev` VMs were created. The
  manifests for all three environments exist and render, but only dev has been
  materialised on a cluster.
- **The promotion workflow and the rollback runbook.** Not yet exercised.

## Note on how the git repo was served during verification

The fork does not exist yet, so Argo CD could not clone from GitHub. To verify
the machinery anyway, the repo was served to the VMs from the host over
`git://192.168.5.2:9418` and the appsets were pointed at it on a throwaway
`local-verify` branch. Everything above is otherwise the real code path — the
same root app, the same ApplicationSets, the same overlays. Only the repository
URL differed; `scripts/set-owner.sh` is what stamps the real one.

An early attempt to serve the repo over dumb HTTP (`python3 -m http.server` plus
`git update-server-info`) failed with `failed to list refs: unexpected EOF` —
Argo CD's git client needs the smart protocol, hence `git daemon`.

---

# Round 2 — three clusters, previews, and the full promotion ladder

## 5. One label, three clusters

`staging` and `prod` were created and registered. Nothing else was done; the
ApplicationSets noticed and generated everything:

```
NAME                        ENV       SYNC        HEALTH        AUTOSYNC
api-service-dev             dev       Synced      Healthy       true
api-service-staging         staging   OutOfSync   Missing       true
api-service-prod            prod      OutOfSync   Missing       <none>     <-- no automated sync
postgres-staging            staging   Synced      Healthy       true
vault-staging               staging   Synced      Healthy       true
postgres-prod               prod      OutOfSync   Missing       <none>
```

`AUTOSYNC <none>` on every prod Application is the `templatePatch` working: the
`automated` block is withheld for prod, so production cannot self-deploy.

## 6. PR previews (trunk-based development)

PR #1 on `feat/preview-demo`, labelled `preview`:

```
$ kubectl --context colima-dev -n pr-feat-preview-demo get pods
api-service-feat-preview-demo-6ffccf9f95-nlbqb         1/1  Running
inventory-service-feat-preview-demo-6ffc98cb98-tcrkg   1/1  Running
postgres-feat-preview-demo-77cbddcc66-64225            1/1  Running
```

The preview serves the PR's own code; dev is untouched:

```
PREVIEW  {"preview":"hello-from-pr","status":"ok","version":"2d20925"}
DEV      {"status":"ok","version":"15bdb10"}          <-- no marker, different build
```

And the `replacements` rewiring works — `DB_HOST` follows the suffixed Service:

```
DB_HOST=postgres-feat-preview-demo
ENVIRONMENT=preview
```

## 7. Promotion moves the artifact, not a rebuild

```
dev     digest: sha256:d5255285e82b5d8f88243c8bea93f05d0ed16b3b51510cdda0fafe6edc0632c3
staging digest: sha256:d5255285e82b5d8f88243c8bea93f05d0ed16b3b51510cdda0fafe6edc0632c3
IDENTICAL
```

Image Updater then wrote the staging tag to git and staging converged with **2
replicas** (staging says 2, dev says 1):

```
Processing results: applications=4 images_considered=4 images_updated=2 errors=0
```

`applications=4` is dev + staging for both services. **Prod is absent** — the
label selector excludes it.

## 8. Production is gated twice, and both gates held

Promoting to prod opened **PR #3** instead of deploying, with a reviewable diff:

```
gitops/apps/api-service/envs/prod/kustomization.yaml  +1/-1
gitops/apps/api-service/envs/prod/settings.yaml       +3/-5
```

After **merging** that PR, prod was still not deployed:

```
api-service-prod   OutOfSync   Missing
$ kubectl --context colima-prod get pods -A | grep -v kube-system
(nothing)
```

Only an explicit sync deployed it, at **3 replicas** (prod 3, staging 2, dev 1).

## 9. The config taxonomy, proven on live clusters

After promoting staging -> prod:

| setting | category | staging | prod | promoted? |
|---|---|---|---|---|
| image version | 1 | 15bdb10 | 15bdb10 | **yes** (same digest) |
| replicas | 2 | 2 | 3 | no |
| `FEATURE_ORDER_LIMIT` | 4 | 100 | **100** (was 25) | **yes** |
| `PAYMENTS_URL` | 3 | `payments.sandbox.example.com` | **`payments.example.com`** | **no** |
| `LOG_LEVEL` | 3 | debug | info | no |
| `ENVIRONMENT` | 3 | staging | prod | no |

The promotion carried the business setting into production and **did not drag the
sandbox payment endpoint with it**. That is the structural guarantee the layout
exists to provide, observed rather than argued.

## Bugs this round surfaced (all fixed, all found by running it)

1. **Per-job tag race.** Each matrix job ran its own `date -u`, so one commit
   produced tags one second apart per service. `promote --service all` failed with
   `inventory-service:dev-...135033Z-eeb5c3b: not found`. The first build had
   landed both jobs in the same second, which is why it hid. Tags are now computed
   once per build.
2. **Duplicate PreSync Job name.** Both services named their gate
   `wait-for-postgres` and both deploy into `shop`, so two Applications owned one
   resource. `HookSucceeded` deletion masked it.
3. **PR head vs merge commit.** On `pull_request`, `actions/checkout` gives the
   merge commit, so `git rev-parse HEAD` was not the PR head the appset derives
   its tag from.
4. **Path filter vs preview completeness.** A preview pins *both* services at
   `pr-<sha>`, but the filter only rebuilt the changed one, so the other had no
   image at that tag. Preview PRs now build the whole stack.
5. **sync-wave deadlock.** Argo CD publishes **no health for ApplicationSet
   resources** (the field is empty), so a `sync-wave: 10` appset waited forever on
   `waiting for healthy state of ApplicationSet/platform-helm` and was never
   created.
6. **`pullRequest.labels` misplaced.** Belongs under `github`. Valid YAML,
   rejected by the API server. `gitops-validate` now checks appsets against the
   real CRD schema and reproduces this exact error.
7. **`slice` on a string.** `slice .head_sha 0 7` fails with "list should be type
   of slice or array but string" and the appset silently generated zero
   Applications. The generator already provides `head_short_sha_7`.
8. **`destination.namespace` is not a namespace override.** The composed bases
   hardcode `shop`/`platform`, which survived into the manifests; the preview
   AppProject correctly refused them. Needs the kustomize namespace transformer.
9. **PreSync gate deadlocks inside one Application.** A preview bundles its own
   postgres, and a PreSync hook runs before that postgres can be created. Both
   gate pods sat Running while nothing else was applied. Previews use sync waves.
10. **`kustomize edit set image` destroys comments** and reformats every list,
    turning a promotion PR into 20 lines of noise. Replaced with a surgical edit
    that produces a one-line diff.

## Still not verified

- **Rollback has not been staged.** The reasoning stands on the mechanism observed
  here ("newest allowed tag wins", watched working three times), but no rollback
  was performed.
- **`inventory-service` was not promoted to prod** — only `api-service`, to keep
  the ladder demonstration short.
- **GitHub Environment reviewer gates are not configured.** `promote.yml`
  references environments `staging` and `prod`, but neither has required
  reviewers, so the prod gate is currently the PR review plus the manual sync,
  not a GitHub approval step.
- One repo setting had to be changed by hand: **Allow GitHub Actions to create and
  approve pull requests** was off, so `gh pr create` failed *after* the retag had
  already happened. Enabled via the API.

---

# Round 3 — External Secrets Operator with Vault as the provider

Replaces per-cluster, per-namespace manual secret seeding, and puts to work the
Vault that had been deployed and entirely unused.

## Verified on the dev cluster

```
NAME                   SYNC     HEALTH
api-service-dev        Synced   Healthy
external-secrets-dev   Synced   Healthy
inventory-service-dev  Synced   Healthy
postgres-dev           Synced   Healthy
vault-config-dev       Synced   Healthy
vault-dev              Synced   Healthy
```

**Vault is persistent, initialised and unsealed by the platform itself:**

```
Initialized     true
Sealed          false
Storage Type    file
persistentvolumeclaim/data-vault-0   Bound   1Gi   local-path
```

Nobody ran `vault operator init` or `vault operator unseal` by hand. The
`vault-config` CronJob did it, and stored the unseal key in
`secret/vault-unseal-keys`.

**No static credential is used to reach Vault:**

```
$ kubectl get clustersecretstore vault
NAME    STATUS   CAPABILITIES   READY
vault   Valid    ReadWrite      True
```

That store contains no token, password or key. ESO presents its own
ServiceAccount JWT and Vault verifies it against the cluster API, using the
`eso` role bound to `external-secrets/external-secrets` with a read-only policy
scoped to one path.

**The credential materialises in every namespace that needs it:**

```
NAMESPACE   NAME                   STORE   STATUS         READY
platform    postgres-credentials   vault   SecretSynced   True
shop        api-service-db         vault   SecretSynced   True
shop        inventory-service-db   vault   SecretSynced   True
```

**And it is genuinely the Vault value, not a coincidence:**

```
vault kv get secret/postgres            -> K1Acti...(24 chars)
shop/api-service-db                     -> K1Acti...(24)
platform/postgres-credentials           -> K1Acti...(24)
MATCH
```

**The application actually authenticates with it:**

```
$ curl localhost:18095/readyz
{"db":"ok","status":"ok","version":"15bdb10"}
```

`db: ok` means api-service opened a real connection to Postgres using a password
that no human has ever seen and that exists nowhere in git.

## Bugs found and fixed this round

1. **AppProject rejected the ESO namespace.** `application destination ...
   namespace external-secrets do not match any of the allowed destinations in
   project platform`. The miss was mine: I added it with a scripted string
   replace that searched for single-quoted `server: '*'` while the file uses
   double quotes, so the edit matched nothing and reported success. Replaces that
   can silently no-op are worse than edits that fail loudly.
2. **StatefulSet volumeClaimTemplates are immutable.** Argo CD's selfHeal
   recreated the old dev-mode StatefulSet from the pre-change git state, then
   could not update it: `Forbidden: updates to statefulset spec for fields other
   than 'replicas' ... are forbidden`. Migrating dev-mode -> file storage requires
   deleting the StatefulSet once. Documented as a migration step.
3. **The reconciler crash-looped on `apk add`.** Installing curl and jq at
   runtime needs root, under `runAsNonRoot: true`. Fixed by removing the
   dependency -- curl plus shell builtins only -- rather than by running as root.
4. **Permanent benign OutOfSync on the Vault StatefulSet.** Live and rendered
   were byte-identical on every field set, but the API server defaults
   `volumeMode` and `status` into volumeClaimTemplates. Resolved with
   `ignoreDifferences` on that path: ignoring an immutable field costs nothing,
   since a diff there can never be resolved by a sync.
5. **ESO caches store validation.** After Vault was unsealed, the store stayed
   `InvalidProviderConfig: Vault is sealed` from a stale check until it
   revalidated. Not a defect, but it means "sealed" errors can outlive the seal.

## Honest limitations

- **The unseal key is stored in a Kubernetes Secret.** That is the direct cost of
  choosing file storage over dev mode: something must unseal Vault unattended.
  Production uses KMS/Transit auto-unseal so no unseal key is ever stored. As it
  stands, anyone who can read Secrets in the `vault` namespace can unseal Vault.
- **Vault is per-cluster, not central.** Each spoke runs its own Vault with its
  own generated password, so "one source of truth for credentials" is true within
  a cluster, not across the fleet. A real deployment points every cluster's
  ClusterSecretStore at one external Vault.
- **A preview now depends on the platform layer.** Previews inherit the
  ExternalSecrets, so they will not come up on a bare cluster -- relevant if
  previews move to their own cluster.
- **Rotation is not wired.** Changing the password in Vault propagates to the
  Secret within a minute, but nothing restarts the pods that read it into env
  vars, so the running processes keep the old value. Real rotation needs either
  `envFrom` with a checksum-triggered rollout, or Reloader.
- **Only dev was migrated.** staging and prod have the manifests but were not
  cut over, so their Vaults are still dev-mode with the old manual secrets.
