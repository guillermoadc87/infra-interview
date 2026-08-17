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
