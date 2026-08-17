#!/usr/bin/env bash
# Thin orchestrator. Each step is independently runnable; this just puts them in
# order for a from-scratch environment.
#
# What changed from the original: this script used to build images and run four
# `helm upgrade --install` commands. It no longer installs ANY workload. Clusters
# come up empty, Argo CD is bootstrapped on the hub, and every spoke provisions
# itself from git the moment it is registered with an `env` label.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

require_tools colima kubectl argocd

: "${GH_OWNER:?set GH_OWNER to the GitHub account that owns your fork}"
: "${GH_PAT:?set GH_PAT to a token with Contents read+write on the repo}"

# Spokes to bring up. Override for a lighter run, e.g. SPOKES="dev" ./scripts/setup.sh
read -r -a SPOKES <<< "${SPOKES:-${SPOKE_PROFILES[*]}}"

info "bringing up the hub"
./scripts/cluster-up.sh "$HUB_PROFILE"

for s in "${SPOKES[@]}"; do
  info "bringing up spoke: $s"
  ./scripts/cluster-up.sh "$s"
done

info "bootstrapping Argo CD on the hub"
./scripts/bootstrap-argocd.sh

warn "log in to Argo CD before registering spokes (register-spoke.sh uses the argocd CLI):"
echo "    kubectl --context $(ctx_for "$HUB_PROFILE") -n argocd port-forward svc/argocd-server 8090:443 &"
echo "    argocd login localhost:8090 --username admin --insecure"
echo
read -r -p "press enter once logged in... " _

for s in "${SPOKES[@]}"; do
  ./scripts/register-spoke.sh "$s"
done

ok "setup complete -- everything from here happens through git"
argocd app list || true
