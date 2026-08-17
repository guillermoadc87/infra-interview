#!/usr/bin/env bash
# Register a spoke cluster with the hub's Argo CD, labelled by environment.
#
# THIS IS THE WHOLE POINT OF THE DESIGN: after this one command, the cluster
# provisions itself. The ApplicationSets select on the `env` label, so labelling a
# cluster `env=dev` causes every platform component and every application with a
# dev overlay to appear on it, with no further instruction.
#
# WHY NOT `argocd cluster add`?
# It performs a SERVER-side connectivity check: the CLI hands the kubeconfig
# server URL to the Argo CD API server, which then dials it. Our kubeconfig
# carries colima's vzNAT address, which is host<->guest only -- so the Argo CD
# server, running inside the hub VM, gets "no route to host" and the command
# aborts *after* having already created the ServiceAccount on the spoke. There is
# no flag to skip that validation.
#
# So we do exactly what the CLI does, minus the unskippable check: create the
# argocd-manager ServiceAccount and RBAC on the spoke, mint a long-lived token,
# and write the cluster Secret ourselves -- pointing at the lima peer address
# that the hub's controllers can actually reach.
# See docs/spike-01-vm-networking.md.
#
# Usage: register-spoke.sh <env>        # dev | staging | prod
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

ENV_NAME="${1:-}"
[ -n "$ENV_NAME" ] || die "usage: $0 <env>   (one of: ${SPOKE_PROFILES[*]})"

require_tools colima kubectl

HUB_CTX="$(ctx_for "$HUB_PROFILE")"
SPOKE_CTX="$(ctx_for "$ENV_NAME")"
FQDN="$(peer_fqdn_for "$ENV_NAME")"
PORT="$(api_port_for "$ENV_NAME")"
[ -n "$PORT" ] || die "could not read the API port for '$ENV_NAME' from kubeconfig -- is the cluster up?"
PEER_URL="https://${FQDN}:${PORT}"

# Fail before touching anything if the hub cannot reach the spoke. Tested from
# INSIDE the hub VM, because that is where the controllers run. Any HTTP status
# proves TCP+TLS completed; 401 is the expected unauthenticated answer.
info "checking hub -> ${PEER_URL} (from inside the hub VM)"
code="$(colima ssh -p "$HUB_PROFILE" -- curl -sk --max-time 8 -o /dev/null -w '%{http_code}' "${PEER_URL}/version" 2>/dev/null || echo 000)"
[ "$code" != "000" ] || die "hub cannot reach ${PEER_URL}. Re-run: ./scripts/cluster-up.sh ${ENV_NAME}"
ok "hub reaches the spoke API (HTTP ${code})"

# --- 1. Argo CD's service account on the spoke -------------------------------
info "creating the argocd-manager ServiceAccount and RBAC on '${ENV_NAME}'"
kubectl --context "$SPOKE_CTX" apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: ServiceAccount
metadata:
  name: argocd-manager
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: argocd-manager-role
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["*"]
  - nonResourceURLs: ["*"]
    verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: argocd-manager-role-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: argocd-manager-role
subjects:
  - kind: ServiceAccount
    name: argocd-manager
    namespace: kube-system
---
# Long-lived token. Since 1.24 a ServiceAccount does not get one automatically,
# so it is requested explicitly via this annotated Secret.
apiVersion: v1
kind: Secret
metadata:
  name: argocd-manager-long-lived-token
  namespace: kube-system
  annotations:
    kubernetes.io/service-account.name: argocd-manager
type: kubernetes.io/service-account-token
EOF

info "waiting for the token to be populated"
for _ in $(seq 1 30); do
  TOKEN="$(kubectl --context "$SPOKE_CTX" -n kube-system get secret argocd-manager-long-lived-token \
    -o jsonpath='{.data.token}' 2>/dev/null || true)"
  [ -n "${TOKEN:-}" ] && break
  sleep 1
done
[ -n "${TOKEN:-}" ] || die "the ServiceAccount token was never populated on '${ENV_NAME}'"
TOKEN="$(printf '%s' "$TOKEN" | base64 -d)"

# --- 2. The cluster Secret on the hub ----------------------------------------
# Argo CD discovers clusters purely from Secrets carrying this label. The `env`
# label is the entire contract between "a cluster exists" and "it provisions
# itself" -- the ApplicationSets' cluster generators select on it.
CA_DATA="$(kubectl config view --raw \
  -o jsonpath="{.clusters[?(@.name==\"${SPOKE_CTX}\")].cluster.certificate-authority-data}")"
[ -n "$CA_DATA" ] || die "could not read the CA for '${SPOKE_CTX}' from kubeconfig"

info "writing the Argo CD cluster Secret (labelled env=${ENV_NAME})"
kubectl --context "$HUB_CTX" -n argocd apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: cluster-${ENV_NAME}
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
    env: ${ENV_NAME}
    managed-by: gitops
type: Opaque
stringData:
  name: ${ENV_NAME}
  server: ${PEER_URL}
  config: |
    {
      "bearerToken": "${TOKEN}",
      "tlsClientConfig": {
        "caData": "${CA_DATA}"
      }
    }
EOF

info "cluster secrets (these labels are what the ApplicationSets select on):"
kubectl --context "$HUB_CTX" -n argocd get secret \
  -l argocd.argoproj.io/secret-type=cluster -L env -L managed-by

ok "'${ENV_NAME}' registered at ${PEER_URL}"
echo "    argocd cluster list      # expect a successful connection"
echo "    argocd app list          # Applications appear with no further action"
