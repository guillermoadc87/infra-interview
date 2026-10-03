# Spike 02 — can a spoke reach the hub?

**Question.** The observability backend runs on the hub
(`gitops/platform/otel-lgtm`), and each spoke's gateway collector has to push
OTLP to it. Every piece of VM-to-VM networking established so far runs the other
way: [`spike-01`](spike-01-vm-networking.md) was about making the hub reach the
spokes' API servers, and `--tls-san` in `scripts/cluster-up.sh` exists for that
direction only. Nothing in the repo showed that a workload on a spoke could open
a connection to the hub at all.

**Answer.** Yes — from inside a pod, by name, to a fixed NodePort. Two
corrections to what I assumed on the way there.

---

## What the network actually looks like

Each colima VM has two addresses:

| Profile | `col0` (vzNAT) | `eth0` (lima user-v2) |
|---|---|---|
| hub | 192.168.64.2 | **192.168.5.1** |
| dev | 192.168.64.3 | 192.168.5.3 |
| staging | 192.168.64.4 | 192.168.5.4 |
| prod | 192.168.64.5 | 192.168.5.5 |

`spike-01` established that the `col0`/vzNAT addresses are not routable between
VMs, and that remains true — a ping from dev to `192.168.64.2` fails. The
`192.168.5.0/24` user-v2 network is the one that carries VM-to-VM traffic, and
`lima-colima-<profile>.internal` resolves to the address on it.

## Two false negatives, recorded because they cost time

Both of these looked like "spoke cannot reach hub" and were measurement errors:

1. **`nc` is not installed in the colima VM.** `nc -z ... && echo OPEN || echo
   CLOSED` printed `CLOSED` for every pair *including a VM to itself*, which is
   what gave it away. `sh: nc: not found` only appears if you look at stderr.
   `/dev/tcp/...` is no better here — the VM shell is dash, which does not
   implement it.
2. **The API server is not on 6443.** colima maps it to a random high port per
   profile (dev 52660, hub 52601, …). Probing 6443 fails everywhere and says
   nothing about reachability. The authoritative source is what Argo CD is
   already using:

   ```console
   $ kubectl --context colima-hub -n argocd get secret cluster-dev \
       -o jsonpath='{.data.server}' | base64 -d
   https://lima-colima-dev.internal:52660
   ```

With the right port, every direction answers `401` — reachable, unauthorised:

```console
hub -> dev:52660       401
dev -> hub:52601       401
dev -> staging:57829   401      # spoke to spoke works too
staging -> hub:52601   401
```

## The result that matters

The API port is explicitly forwarded by colima, so it proves little about an
arbitrary service. The real test is a NodePort, reached from a **pod**:

```console
$ kubectl --context colima-hub -n argocd expose deployment argocd-server \
    --name=spike02-nodeport --type=NodePort --port=8080   # -> nodePort 30913

$ kubectl --context colima-dev run spike02 --rm -i --restart=Never \
    --image=curlimages/curl:8.11.1 --command -- sh -c '
      getent hosts lima-colima-hub.internal
      curl -s -o /dev/null -w "%{http_code}\n" http://lima-colima-hub.internal:30913/'
192.168.5.1       lima-colima-hub.internal
307
```

`307` is argocd-server redirecting HTTP to HTTPS — a real response from the hub.
So:

- a pod on a spoke **resolves** `lima-colima-hub.internal`, because CoreDNS
  forwards names it does not own to the node's resolver, which has it;
- a pod on a spoke **reaches** the hub's NodePort range over user-v2.

One more false negative worth knowing: the same `curl` run on the VM itself (via
`colima ssh`) fails, while from a pod it succeeds. Not investigated, because the
pod is the only case the design needs — but it means a host-level probe is not a
valid test of this path.

## What this decided

- `otel-lgtm` runs on the hub, exposed by a **NodePort with fixed numbers**
  (30317 OTLP gRPC, 30318 OTLP HTTP, 30300 Grafana). Fixed because a spoke's
  collector names them in its values file, so an auto-allocated port would
  change on recreation and silently break every spoke.
- A spoke's gateway exports to `lima-colima-hub.internal:30317` — by name, not
  by IP, so nothing depends on colima's address assignment.
- The hub needed registering with Argo CD as a deploy target, which it never was.
  It is now a cluster with `env: hub`, exactly like a spoke has `env: dev`, so
  `gitops/platform/otel-lgtm/envs/hub/` is picked up by the same ApplicationSet
  with no appset change. See `scripts/bootstrap-argocd.sh`.

**And what it deliberately did not decide.** This path is used for *viewing
only*. The gateway collector also exposes a Prometheus endpoint in-cluster, and
that is what Argo Rollouts queries for canary analysis — so a rollback decision
never depends on anything in this document being true. The verification log
proves that by scaling `otel-lgtm` to zero and re-running a failed rollout.
