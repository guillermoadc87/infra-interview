#!/usr/bin/env bash
# Create an EMPTY Kubernetes cluster. Installs no workloads whatsoever.
#
# This is the deliberate split from the original setup.sh, which conflated
# "make me a cluster" with "helm install four things onto it". Everything that
# runs on a cluster now arrives through Argo CD from git; the only thing this
# script produces is a bare k3s node.
#
# Usage: cluster-up.sh <profile>        # hub | dev | staging | prod
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

PROFILE="${1:-}"
[ -n "$PROFILE" ] || die "usage: $0 <profile>   (one of: ${ALL_PROFILES[*]})"

require_tools colima kubectl

if profile_running "$PROFILE"; then
  ok "colima profile '$PROFILE' is already running"
else
  # Overridable because four VMs on a 32 GB laptop is genuinely tight. A spoke
  # runs postgres + two small Go services + dev-mode Vault, so 3 GiB is ample;
  # the hub runs Argo CD and wants more.
  CPUS="${CPUS:-2}"
  MEMORY="${MEMORY:-4}"
  info "starting colima profile '$PROFILE' (k3s, ${CPUS} cpu, ${MEMORY} GiB)"

  # --network-address gives the VM a routable address. Without it colima leaves
  # the kubeconfig pointing at 127.0.0.1, which a pod on another VM would
  # resolve to itself -- registration would appear to succeed and never sync.
  args=(
    --profile "$PROFILE"
    --runtime docker
    --kubernetes
    --cpu "$CPUS" --memory "$MEMORY" --disk 20
    --network-address
    # k3s bundles its own Traefik, installed from a HelmChart CR in kube-system.
    # That copy exists in no git repository, so it directly contradicts the
    # claim this script's header makes -- that the only thing produced here is a
    # bare node and everything else arrives through Argo CD. It is also the one
    # pre-existing workload that was never accounted for anywhere.
    #
    # gitops/platform/traefik replaces it. servicelb (klipper) is deliberately
    # NOT disabled: it is what assigns the Traefik Service the VM's own address,
    # which is what makes the app reachable without a port-forward.
    #
    # The packaged manifest declares TWO HelmCharts, `traefik-crd` and
    # `traefik`, and this flag removes the manifest so both go away -- including
    # the Gateway API v1.3.0 CRDs that traefik-crd installs. Deliberate:
    # gitops/platform/gateway-api owns those CRDs and pins a different version.
    #
    # NOTE this only affects clusters created from here on. An existing cluster
    # keeps the bundled copies until you either recreate it or delete BOTH --
    # dropping only `traefik` leaves the v1.3.0 CRDs behind, Helm-owned, and
    # Argo CD then fails to adopt them with a field-ownership conflict:
    #   kubectl -n kube-system delete helmchart traefik traefik-crd
    --k3s-arg="--disable=traefik"
  )

  # Spokes must be reachable FROM the hub VM. That traffic goes over lima's
  # user-v2 network, and colima only puts the (unreachable) vzNAT address in the
  # serving cert -- so we add the reachable name ourselves or TLS verification
  # fails. See docs/spike-01-vm-networking.md.
  if [ "$PROFILE" != "$HUB_PROFILE" ]; then
    args+=( --k3s-arg="--tls-san=$(peer_fqdn_for "$PROFILE")" )
  fi

  colima start "${args[@]}"
fi

info "waiting for the node to become Ready"
kubectl --context "$(ctx_for "$PROFILE")" wait --for=condition=Ready nodes --all --timeout=300s >/dev/null
ok "cluster '$PROFILE' is up and EMPTY"

kubectl --context "$(ctx_for "$PROFILE")" get nodes -o wide
