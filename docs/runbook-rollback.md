# Runbook — rolling back dev and staging

Staged end to end on the dev cluster. Every output below is real.

## The claim being tested

> `git revert` does not roll back dev or staging. Those environments run on
> "the newest allowed tag in the registry wins", so reverting the tag bump means
> Image Updater re-reads the registry, still finds the bad tag newest, and writes
> it straight back.

It is true. The mechanism is more specific than that sentence suggests, and the
detail matters when you are staring at it at 2am.

## Setting up a realistic failure

A bug that **health checks and tests cannot catch** — `LIMIT 100` changed to
`LIMIT 1` in the order list query. No test covered `listOrders` (it needs a
database), so:

```
CI PASSED (the bug shipped)
```

Deployed by the normal pipeline, the pod is entirely healthy:

```
api-service-6495475746-kqhzt   1/1   Running

$ curl /healthz   {"status":"ok","version":"c09c3f7"}
$ curl /readyz    {"db":"ok","status":"ok","version":"c09c3f7"}

$ curl /orders    orders returned: 1     <- was 3
```

This is the case rollback exists for. Probes protect against a deploy that
*breaks*; they do nothing about a deploy that is *healthy and wrong*.

## What `git revert` actually does

Reverting Image Updater's own commit:

```
$ git revert b5c0f3d      # "build: automatic update of api-service-dev"
-    newTag: dev-20260817T193022Z-c09c3f7
+    newTag: dev-20260817T135450Z-15bdb10
```

**For three minutes, nothing happened.** The revert sat in git, untouched:

```
  t+15 s  newTag=dev-20260817T135450Z-15bdb10
  ...
  t+180s  newTag=dev-20260817T135450Z-15bdb10
```

That is not the revert winning. Image Updater compares the registry against the
**live Application**, which was still running the bad tag — so from its point of
view there was nothing to do. The revert was simply not yet in effect.

Then Argo CD synced it, the deployment rolled back to the good image, and:

```
  t+20 s  newTag=dev-20260817T193022Z-c09c3f7
  >>> UNDONE: Image Updater wrote the bad tag back
```

**Twenty seconds.** The moment the revert took effect, Image Updater saw the
running image was older than the registry's newest and undid it. Confirmed:

```
deployed=ghcr.io/guillermoadc87/api-service:dev-20260817T193022Z-c09c3f7
orders returned: 1
```

So the failure mode is worse than "the revert does not work". The revert *works*,
briefly, and is then reversed — which looks like the cluster fighting you, and
leaves a git history of a revert followed by an automated re-apply.

## The rollback that works: roll the registry forward

`.github/workflows/rollback.yml` republishes a **known-good digest** under a new,
higher-sorting tag for the same environment:

```
$ gh workflow run rollback.yml -f service=api-service -f environment=dev \
    -f good_tag=dev-20260817T135450Z-15bdb10

known-good digest: sha256:d5255285e82b5d8f88243c8bea93f05d0ed16b3b51510cdda0fafe6edc0632c3

dev-20260817T193022Z-c09c3f7      <- the bad build
dev-20260817T194146Z-15bdb10      <- same digest as the good build, newer timestamp
```

The original commit sha (`15bdb10`) is kept in the tag, so the audit trail still
says which source produced the running image; only the timestamp moves.

Now the "newest tag wins" rule works *for* you:

```
  t+45 s  newTag=dev-20260817T194146Z-15bdb10
  >>> Image Updater adopted the rolled-back image

deployed=ghcr.io/guillermoadc87/api-service:dev-20260817T194146Z-15bdb10
$ curl /healthz   {"status":"ok","version":"15bdb10"}
$ curl /orders    orders returned: 3
```

**And it stays.** Ninety seconds later the tag is unchanged — unlike the revert,
which lasted twenty seconds. Nothing newer exists, so nothing overrides it.

Recovery time is one Image Updater poll, roughly a minute.

## Rollback restores service; it does not fix the bug

After all of the above, the bad code was still on `main`. The next unrelated
commit would have rebuilt and redeployed it. Rollback buys time — the fix still
has to go forward through the normal pipeline:

```
$ git revert c09c3f7      # the SOURCE commit, not the tag bump
```

Note this is a different revert from the one that failed. Reverting **source**
works fine: CI rebuilds and the new image legitimately becomes newest. Reverting
the **tag bump** is what Image Updater undoes. Distinguishing the two is the
practical takeaway.

## Production is not like this

Prod has no Image Updater and syncs manually, so nothing races you: `git revert`
plus a sync is a true rollback there. That asymmetry is a large part of why prod
is gated the way it is — the environments that optimise for speed pay for it with
a counterintuitive rollback, and the environment that optimises for
auditability gets the obvious one.

## Break-glass

To stop deployments entirely rather than change them:

```bash
kubectl --context colima-hub -n argocd scale deploy/argocd-image-updater-controller --replicas=0
# now git revert behaves normally
```

Remember to scale it back, or dev and staging quietly stop tracking the registry.
