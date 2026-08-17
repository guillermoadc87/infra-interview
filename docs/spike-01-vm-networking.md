# Spike 01 — Can Argo CD on a hub colima VM reach a spoke's k3s API?

**Date:** 2026-08-16
**Status:** RESOLVED — hub-and-spoke is viable, with two required changes.
**Why it mattered:** the entire multi-cluster topology depends on this. If it had
failed, the fallback was one cluster with three namespaces, which changes only
the bootstrap scripts — the `gitops/` tree is identical either way.

## Question

Colima assigns each VM a reachable address with `--network-address` and writes it
into the kubeconfig. Can a pod on the `hub` VM open a TCP connection to the k3s
API server on the `dev` VM using that address?

## Method

Two profiles, `hub` and `dev`, each `--kubernetes --cpu 2 --memory 4 --network-address`.

## Findings

### 1. The address colima writes into kubeconfig does NOT work between VMs

```
$ colima list
hub   Running   aarch64   2   4GiB   20GiB   docker+k3s   192.168.64.2
dev   Running   aarch64   2   4GiB   20GiB   docker+k3s   192.168.64.3

$ curl -sk https://192.168.64.3:52660/version        # from the HOST
http_code=401                                         # PASS

$ colima ssh -p hub -- curl -sk https://192.168.64.3:52660/version
http_code=000  (exit 7)                               # FAIL — connection refused
```

`192.168.64.x` is **vzNAT**, which is a host↔guest network only. Guests are
isolated from each other on it. This matches lima's own network documentation,
which routes VM↔VM traffic over `user-v2` instead.

### 2. There is a second network that DOES work — `user-v2`

Both VMs carry two interfaces:

```
hub:  eth0 192.168.5.1/24 (user-v2)   col0 192.168.64.2/24 (vzNAT)
dev:  eth0 192.168.5.3/24 (user-v2)   col0 192.168.64.3/24 (vzNAT)
```

k3s binds `*:52660` (all interfaces), so it is listening on the user-v2 address
too, and lima provides DNS for it:

```
$ colima ssh -p hub -- getent hosts lima-colima-dev.internal
192.168.5.3     lima-colima-dev.internal

$ colima ssh -p hub -- curl -sk https://lima-colima-dev.internal:52660/version
http_code=401                                         # PASS
```

`401` is the correct success signal here: TCP and TLS completed and the API
server answered. Argo CD authenticates with a bearer token, so an unauthenticated
401 is exactly the reachability proof needed.

Note the bare hostnames `colima-dev` and `lima-colima-dev` do **not** resolve —
only the fully-qualified `lima-colima-dev.internal`.

### 3. The default k3s cert does not cover the working address

```
SANs before: DNS:colima-dev, DNS:kubernetes[...], IP:10.43.0.1, IP:127.0.0.1, IP:192.168.64.3
```

Colima launches k3s with `--advertise-address <vzNAT IP>` and adds no `--tls-san`,
so the certificate covers the address that *doesn't* work and omits the one that
does. TLS verification over user-v2 therefore failed (curl exit 60).

**Fix — start each spoke with explicit SANs:**

```bash
colima start -p dev --kubernetes --network-address \
  --k3s-arg='--tls-san=lima-colima-dev.internal' \
  --k3s-arg='--tls-san=192.168.5.3'
```

```
SANs after: ... DNS:lima-colima-dev.internal, IP:192.168.5.3, IP:192.168.64.3 ...
```

### 4. Verified end state

With the real k3s CA from the kubeconfig, and full hostname verification:

```
$ colima ssh -p hub -- curl -s --cacert /tmp/dev-ca.crt \
    https://lima-colima-dev.internal:52660/version
verified_http=401
```

TLS chain and hostname both validate. **No `tlsClientConfig.insecure: true` is
needed**, which is worth having: an insecure registration would have hidden any
future certificate problem.

## Consequences for the implementation

1. `scripts/cluster-up.sh` must pass `--tls-san=lima-colima-<profile>.internal`
   when starting a spoke.
2. `scripts/register-spoke.sh` must **not** register the kubeconfig context as-is.
   Colima's context points at the vzNAT address, which Argo CD cannot reach. The
   script rewrites the server URL to the user-v2 FQDN before `argocd cluster add`.
3. The k3s port is per-profile and random (`52660` here), so it must be read from
   the kubeconfig rather than assumed to be 6443. It did survive a stop/start.

## Caveat

The user-v2 addresses (`192.168.5.x`) are assigned by lima and were stable across
one restart, but I have not verified they are stable across a delete/recreate.
The scripts therefore key on the `.internal` hostname, not the IP, everywhere it
is possible to do so.
