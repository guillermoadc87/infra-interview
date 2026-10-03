# How complete is this as a local Kubernetes deployment?

**~6.5 / 10 as a complete deployment. ~9 / 10 as a GitOps delivery system.**

That gap is the whole finding. What this repository built is a top-decile
delivery *control plane* attached to a cluster with no front door, no eyes and
no guardrails. Every weak axis below is a data-plane or platform concern, not a
delivery one — which is exactly what you would expect from a repo that took
Path 4 of 4 and left Paths 2 (monitoring) and 3 (security) untouched.

This document scores the starting point, then describes what the platform layer
added and what it deliberately did not.

---

## 1. Scored, axis by axis

| Axis | | Evidence |
|---|---|---|
| GitOps / delivery | **9** | Three-level app-of-apps; matrix (git × clusters) ApplicationSets; the environment is read out of the repo path; a cluster provisions itself from one `env` label; PR previews; AppProjects with an *enumerated* `clusterResourceWhitelist` |
| Promotion / release engineering | **9** | Retags a digest and never rebuilds; asserts the digest is unchanged and that `linux/arm64` exists; resolves the tag *from git* rather than from a form; prod is ungoverned by Image Updater and PR-gated; rollback rolls *forward*, with `rollback.yml` explaining why `git revert` cannot work under an image updater |
| Config management | **9** | The four-category taxonomy (`envs/` vs `variants/`) is better than most production shops. Category 3 sits *physically outside* `envs/`, so "a promotion cannot leak the sandbox payments URL" is structural, not a convention |
| Docs / runbooks | **9** | `OVERVIEW.md`, `gitops/README.md`, two runbooks, a 590-line verification log, a networking spike. Comments explain *why*, including bugs the author found in their own work |
| Secrets | **8** | ESO + Vault Kubernetes auth; the `ClusterSecretStore` holds no credential; per-application dynamic Postgres roles on a 1h TTL bound to per-app ServiceAccounts. Deductions: the unseal key sits in a plain Secret, and env-var injection forces a pod restart every ~45 minutes |
| Validation / testing | **7** → **8** | `kustomize build` + `kubeconform` over every overlay plus six bespoke structural gates — including an AST-ish check that every Image Updater reference sits inside the non-prod branch. Deduction at the time: `-ignore-missing-schemas` meant *every* custom resource was silently skipped. Now fixed (§3) |
| Cluster provisioning | **6** | Empty-by-design, scripted, pinned versions, clean teardown, a load-bearing `--tls-san` fix. Deductions: imperative bash rather than a declarative cluster spec; macOS/Apple Silicon only; ~14 GiB; and k3s's *bundled* Traefik quietly contradicted "clusters come up EMPTY" |
| Workload reliability | **6** → **7** | A correct liveness/readiness split (`/healthz` never touches the database), `maxUnavailable: 0`, a PDB, a PreSync gate, opt-in Reloader. Deductions: **no autoscaling of any kind** (k3s ships metrics-server and nothing used it), no topology spread, no PriorityClass, and prod's `replicas: 1` with `minAvailable: 1` is an availability *blocker* on drain |
| Supply chain | **4** | Multi-stage cross-compiled non-root images with uid 10001 matching `runAsUser`; pinned chart versions; digest-preserving promotion. Deductions: `provenance: false`, `sbom: false`, nothing signed, nothing scanned — and `promote.yml:9` references `trivy` as though scanning existed |
| Data / storage | **3** | Postgres is a plain Deployment on `emptyDir` (a deliberate "dev fixture" decision); Vault has a PVC on local-path. No backup, no PITR, no operator |
| East-west / policy | **2** → **6** | PSS `baseline` via `managedNamespaceMetadata`, least-privilege Vault policies, enumerated AppProject allowlists. But **zero NetworkPolicies**, no admission control, no mTLS — and `argocd-manager` holds `*/*/*` cluster-admin on every spoke |
| Observability | **1** → **7** | Nothing at all. No metrics, logs, traces or alerts, and no Argo CD notifications: a failed sync was visible only to someone looking at the UI |
| North-south networking | **1** → **6** | No Ingress, no Gateway, no TLS. `OVERVIEW.md` verified a deployment with `kubectl exec … wget -qO- localhost:8080`, and the PR-preview feature built a complete isolated stack per pull request **that nobody could open in a browser** |

Arrows mark what this change moved. Nothing reached 10, and the three axes still
at 6 or below (supply chain, storage, provisioning) are untouched on purpose —
see §5.

### Three documentation defects

Worth fixing because the repo's credibility rests on its prose being accurate:

- `SOLUTION.md` "Production Considerations" still said secrets were *"Not
  implemented"* and that Vault *"is a toy… dev mode is in-memory"*, and Future
  Improvement #3 still proposed ESO + Vault. All three were superseded by the
  work `OVERVIEW.md` §4 describes.
- `gitops/bootstrap/spoke/postgres-credentials.example.yaml` still said External
  Secrets *"is NOT implemented here"*. It is.
- `Makefile` and `TROUBLESHOOTING.md` reference profile `dfns-interview` and
  namespace `interview-test`; `make deploy` still runs the four
  `helm upgrade --install` commands the solution replaced. (Self-acknowledged in
  `OVERVIEW.md` §6.)

---

## 2. Why adding a controller is cheap here

Credit where it is due: this was the easiest platform layer to extend I could
have asked for. `platform-helm-applicationset.yaml` matrix-generates from
`gitops/platform/*/envs/*/config.json`, and the Kustomize one keys on
`kustomize.json` — the *filename* routes a component to one or the other. So a
new controller is a directory, and **no ApplicationSet changed in this entire
piece of work**.

Six constraints bite, all of them discovered the hard way:

1. `ignoreMissingValueFiles` is deliberately unset, so a Helm component needs an
   `envs/<env>/values.yaml` for every environment it targets.
2. CI asserts `appName` equals path segment 2, so the directory name and
   `appName` must match.
3. The `platform` AppProject enumerates destinations. A component in a new
   namespace is rejected *before it ever syncs* — that is the first thing to
   check when a new component appears to do nothing.
4. Prod platform Applications get no `automated` block, so each needs one manual
   `argocd app sync`.
5. `platform-kustomize` stamps `pod-security.kubernetes.io/enforce: baseline` on
   every namespace it creates. **Baseline forbids hostPath and hostPort**, which
   shaped the collector design more than anything else here.
6. Helm lands in sync-wave `-20` and Kustomize in `-10`, which gives
   "chart first, then its custom resources" for free.

---

## 3. What was added

Nine components, all through the existing mechanism.

### A front door — `traefik` + `gateway-api`

Gateway API CRDs (`GatewayClass`, `Gateway`, `HTTPRoute`, `GRPCRoute`,
`ReferenceGrant`) pinned to v1.6.2, and Traefik 41.6.1 serving them.
`scripts/cluster-up.sh` now passes `--k3s-arg=--disable=traefik`, because the
bundled copy exists in no git repository and was the one pre-existing workload
nothing accounted for.

Each service gets an `HTTPRoute` in `base/`, so every environment has one even
where no controller watches it yet — the CRDs are installed everywhere while
Traefik is dev-only, which means promoting Traefik later needs no application
change.

**Previews get their own URL.** The appset injects a per-PR path prefix, so N
open pull requests do not all claim `/api` and silently serve each other's
branches.

### Admission enforcement — `kyverno` + `kyverno-policies`

Four `ClusterPolicy` objects: pod hygiene (probes, requests), immutable tags from
this repo's registry only, the telemetry contract, and — the reason it is Kyverno
rather than Kubernetes' built-in `ValidatingAdmissionPolicy` — a `generate` rule
that puts a default-deny NetworkPolicy and a ResourceQuota into **every preview
namespace as it is created**. Preview namespaces appear on demand, so there is no
commit in which to add their NetworkPolicy and no moment to label them first; a
generate rule is the only mechanism that reaches them. That closes the
zero-NetworkPolicy gap and bounds preview cost, which the appset's own comment
flagged as unbounded.

Every validating policy ships in **Audit**, not Enforce. Enforcing "must declare
requests" on day one would reject pods from the charts this platform already
depends on. The rollout is: ship Audit, read `kubectl get polr -A`, fix or except
what it finds, then flip per policy. The admission webhook also runs
`failurePolicy: Ignore` — on a 2-CPU node, `Fail` turns a Kyverno restart into a
cluster-wide outage, and these are guardrails rather than a security boundary.

### Automated rollback — `argo-rollouts`

`SOLUTION.md` stated the gap: *"A failed PostSync hook makes the sync loudly red;
it does not roll back."* Each service now has a `Rollout` and an
`AnalysisTemplate` that queries success rate and aborts by itself.

**The Rollout uses `workloadRef`; the Deployment stays a Deployment.** Converting
`base/deployment.yaml` to `kind: Rollout` was the first attempt and it silently
destroys this repo's config model. Kustomize resolves strategic-merge patches
with the built-in OpenAPI schema and has none for a CRD, so list fields are
*replaced* rather than merged by key. Measured:

```console
$ kustomize build over/      # patch adds one env var to a Rollout
containers:
- env:
  - name: ENVIRONMENT
    value: dev
  name: api-service          # image, ports, probes, resources, other env: GONE
```

Every file under `variants/` and `envs/` patches the workload, so all of them
would have quietly gutted it. `workloadRef` sidesteps it: the pod template keeps
living where kustomize knows how to merge it, and the Rollout carries only
strategy. The one patch that did move is `replicas.yaml`, because under
`workloadRef` the Rollout owns the replica count.

dev and staging run a real canary with analysis. **Prod does not, and cannot** —
one replica on a 2-CPU node, and not enough traffic to judge a canary. It gets a
steps-free strategy, which behaves as the rolling update it already had. Fixing
that needs more nodes, not more config.

### Observability — OpenTelemetry, three tiers, plus `pkg/obs`

```
app process ── pkg/obs SDK, OTLP
     ▼
tier 1  otel-agent    DaemonSet   k8sattributes, kubeletstats
     ▼
tier 2  otel-gateway  Deployment  tail_sampling, k8s_cluster, redaction
     ├── :9090 Prometheus  ◀── Argo Rollouts queries HERE
     ▼ OTLP
tier 3  otel-lgtm     on the hub  Grafana + Prometheus + Tempo + Loki
```

The tiers are not decoration. Tail sampling **cannot** be done per-node: a
decision needs every span of a trace in one place, and spans of one trace can be
produced on different nodes, so a per-node sampler would keep half a trace and
drop the rest. Cluster metrics conversely must be collected once, not per node.

**The gateway exports twice, and that is the important property.** OTLP to the
hub for humans, and a local Prometheus endpoint for the rollout decision. So
canary analysis and automatic rollback never depend on cross-VM networking —
only viewing does. `docs/spike-02-spoke-to-hub.md` records what had to be proven
to make tier 3 work at all; spike-01 had only ever established the hub → spoke
direction.

`pkg/obs` is the shared library, and it owns the standards: resource attributes
assembled in one place, semconv metric names, 100% sampling at the SDK (because
the sampling decision belongs to the gateway), and logs correlated to traces.
Enforcement is three-layer — the library is the only door, CI fails the build if
anything under `apps/` imports `go.opentelemetry.io` directly, and the Kyverno
policy rejects a pod with no OTLP endpoint.

Two decisions inside it are worth stating because they look like omissions:

- **`/healthz` and `/readyz` are excluded from traces and metrics.** Not
  tidiness: the kubelet produces more requests than this application ever will,
  and `/readyz` answers 503 *by design* while a pod connects to Postgres.
  Counted as server errors, every Argo CD sync would spike the measured error
  rate and abort a canary that is behaving perfectly.
- **No `filelog` or `hostmetrics` receiver.** Both need hostPath, and constraint
  5 above forbids it. Logs come from the SDK instead — which is what makes the
  shared library load-bearing rather than decorative. The honest cost: Postgres
  and Vault contribute no logs. Collecting those means one privileged,
  tightly-scoped namespace, and that should be a decision rather than a default.

### And one bug found by actually running it

The first version of `pkg/obs` returned a concrete handler from `Logger()`.
Both services do `var Logger = obs.Logger()` at package level, which runs before
`main()` and therefore before `Init()` — so the variable bound itself to the
stdout-only handler and every record afterwards went to stdout and nowhere else.
Forty error logs, visible in `kubectl logs`, absent from Loki, nothing anywhere
reporting a problem. Verified against a real backend, fixed with a late-binding
handler, and pinned by `TestALoggerCapturedBeforeInitStillReachesTheProvider`.

### CI

`kubeconform` lost `-ignore-missing-schemas` and gained the CRDs-catalog, so
custom resources are now genuinely validated instead of skipped — **150
resources across all 29 overlays, zero skipped**. `CustomResourceDefinition` is
skipped *explicitly*, because kubeconform ships no schema for it at any version.

`ci.yml` moved the Docker build context to the repository root (the `replace`
directive for `pkg/obs` is invisible from `apps/<svc>/`) and its change
detection now matches `pkg/obs/`. That second one matters: the comment there
argued that "an image's content comes from the service's source and its
Dockerfile, both under `apps/<svc>/`", which stopped being true the moment a
shared library was compiled into both binaries. Left unmatched, editing the
observability library would have rebuilt nothing.

---

## 4. What is verified, and what is not

Verified by running it:

- Both images build with the new root context; the container starts, serves,
  and exits 0 on SIGTERM.
- `pkg/obs` end to end against a real `grafana/otel-lgtm`: the semconv histogram
  `http_server_request_duration_seconds_count` carries `service_name`,
  `service_version`, `deployment_environment_name`, `k8s_pod_name` and
  `http_route`; traces land in Tempo named by route; logs land in Loki with
  `trace_id` and `span_id` present. Probe paths absent from every series.
- All 29 overlays build and validate strictly.
- Every pre-existing CI gate still passes.
- The import-boundary gate fires on a planted violation and is silent on a clean
  tree.
- Spoke → hub reachability, from a pod, by name, to a NodePort.

**Not verified, because it needs the full cluster:** the canary actually aborting
on a bad image; the generated NetworkPolicy actually blocking cross-namespace
traffic; and the credential-rotation chain surviving the Rollout conversion —
Reloader patches the Deployment and Argo Rollouts should notice. That last one is
the single most important cluster check, because if the reasoning in
`gitops/platform/reloader/base/values.yaml` is wrong, rotation fails silently.

---

## 5. What was left out, and why

Ranked, with the reason each is not here rather than a vague "future work".

**Deferred because of the resource budget** (4 VMs, 2 CPU, 3–4 GiB each):

- **cert-manager** (`Certificate`, `ClusterIssuer`) — wanted, and the reason the
  OpenTelemetry *Operator* is not here. The operator's admission webhook needs a
  TLS certificate; with cert-manager disabled the chart generates one with
  Helm's `genSignedCert`, which produces a different certificate on every
  render. Argo CD renders on every reconcile, so the Secret and the webhook's
  `caBundle` would be permanently OutOfSync with no sync able to settle them.
  Fixing it properly means cert-manager, i.e. three more pods. The plain
  collector chart delivers the same pipeline, so the operator is a deliberate
  deferral — it lands with cert-manager, and `OpenTelemetryCollector` /
  `Instrumentation` come with it.
- **A service mesh** (Linkerd, or Istio ambient) for mTLS and free L7 metrics.
  Does not fit 2 CPU. This is the honest answer to east-west encryption, which
  the generated NetworkPolicies only approximate.

**Deferred because the cluster cannot demonstrate them:**

- **A real canary in prod.** One replica, 2 CPU, no traffic. Needs nodes.
- **HPA / KEDA.** There is no event source, and autoscaling a service with no
  load is theatre. The right shape once there is traffic is HPA against the
  OTel-derived metrics the gateway already exposes.

**Deferred because they belong to other paths, and are worth more than a gesture:**

- **CloudNativePG** (`Cluster`, `ScheduledBackup`, `Pooler`) — the single
  highest-value remaining item. It replaces the `emptyDir` Postgres, answers
  `SOLUTION.md` Future Improvement #7, and pairs with the existing Vault dynamic
  credentials.
- **Supply chain**: cosign signing plus `trivy-operator` (`VulnerabilityReport`,
  `ConfigAuditReport`), which would make `promote.yml`'s existing `trivy`
  reference real. Scoring 4 here is the lowest real score in the table and the
  cheapest to lift.
- **Velero** (`Backup`, `Schedule`) for the PVC data git cannot reconstruct —
  which now includes the telemetry backend's own storage.
- **Argo CD Notifications.** Ships with Argo CD and needs only configuration.
  The cheapest fix on this list for "a failed sync is visible only to someone
  looking at the UI".

**Deliberately not recommended:**

- **Sealed Secrets / SOPS** — redundant; ESO + Vault already covers it.
- **external-dns** — nothing local for it to manage.
- **Crossplane** — the ideologically pure answer to the orphaned
  `terraform/main.tf`, and genuinely interesting, but there is almost nothing in
  a laptop cluster for it to reconcile.
- **vcluster or Cluster API** would make cluster creation declarative and cut
  the RAM budget substantially. The most interesting item on this list, and a
  rewrite of `scripts/` rather than an addition to `gitops/`.
