# Runbook — rotating the database credential

Observed end to end on the dev cluster. Every output below is real.

## The gap this closes

External Secrets updates the Kubernetes Secret within a minute of a Vault
change, but **environment variables are read once at container start**. Without
something to restart the pods, a rotation updates the Secret and the running
processes keep serving with the old value — the rotation silently does nothing.

Reloader watches the Secret named on each Deployment and triggers a normal
rolling update when it changes.

## The procedure

### 1. Rotate in Vault

```bash
vault kv put secret/postgres username=postgres password='<new>'
```

### 2. External Secrets propagates (~20s observed, refreshInterval 1m)

```
ESO updated the Secret after ~20s
```

### 3. Reloader rolls the consumers, automatically

```
Changes detected in 'api-service-db' of type 'SECRET' in namespace 'shop';
  updated 'api-service' of type 'Deployment' in namespace 'shop'
Changes detected in 'inventory-service-db' of type 'SECRET' in namespace 'shop';
  updated 'inventory-service' of type 'Deployment' in namespace 'shop'
```

### 4. The new pods FAIL — and that is correct

```
api-service-5bbd9bbf64-rgg8j   0/1   Running    2m27s   <- new, cannot authenticate
api-service-6468bdb9cd-c6x29   1/1   Running    72m     <- old, still serving

$ kubectl logs api-service-5bbd9bbf64-rgg8j
database not ready (attempt 7): pq: password authentication failed for user "postgres"
```

The new pods hold the new password; **the database still has the old one**.
Nothing has rotated the database itself.

**There was no outage.** `maxUnavailable: 0` plus a readiness probe means the new
pod never entered the Service, so the old pod kept serving throughout:

```
$ curl /readyz        # during the stuck rollout
{"db":"ok","status":"ok","version":"15bdb10"}
```

The rollout simply stops and waits, visibly:

```
Waiting for deployment "api-service" rollout to finish: 1 old replicas are pending termination...
```

This is the same safety property that protects a bad image, doing its job for a
bad credential.

### 5. Rotate the database itself

```bash
kubectl -n platform exec <postgres-pod> -- \
  psql -U postgres -c "ALTER USER postgres PASSWORD '<new>';"
```

`ALTER USER`, **not** a pod restart. `POSTGRES_PASSWORD` is only read when the
data directory is first initialised, so restarting would not adopt the new
password on a real volume — and on this emptyDir it would wipe the database
outright. That is why the postgres Deployment is deliberately not annotated for
Reloader.

### 6. The stuck rollout completes on its own

```
ALTER ROLE
deployment "api-service" successfully rolled out

api-service-5bbd9bbf64-rgg8j   1/1   Running       <- new pod now Ready
api-service-6468bdb9cd-c6x29   1/1   Terminating   <- old pod drains
```

Verified afterwards:

```
$ curl /readyz
{"db":"ok","status":"ok","version":"15bdb10"}

pod env DB_PASSWORD == rotated Vault value   (ROTATED8...)
```

## What this exposes

Steps 4 and 5 are the real lesson. **A static database credential cannot be
rotated atomically**: there is a window where the consumers have the new password
and the database has the old one. Here that window is safe, because the rollout
blocks rather than breaking — but it is still a window, and closing it by hand
does not scale.

The complete answer for database credentials is **Vault's database secrets
engine**: Vault connects to Postgres as an admin, issues a short-lived unique
credential per application on demand, and revokes it on expiry. There is no
shared password to rotate, so the ordering problem disappears. ESO consumes those
dynamic credentials the same way it consumes static ones.

Reloader remains the right tool for everything that is not a database password —
API keys, tokens, TLS certificates — where the consumer is the only party that
needs to learn the new value.

## Scope

Wired and verified on **dev** only. staging and prod have the manifests but were
not cut over.
