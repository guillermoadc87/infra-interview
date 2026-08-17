#!/usr/bin/env bash
# Shared helpers. Sourced, not executed.

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;36m'; NC='\033[0m'

info()  { printf "${BLUE}==>${NC} %s\n" "$*"; }
ok()    { printf "${GREEN}OK${NC} %s\n" "$*"; }
warn()  { printf "${YELLOW}warning:${NC} %s\n" "$*" >&2; }
die()   { printf "${RED}error:${NC} %s\n" "$*" >&2; exit 1; }

HUB_PROFILE="hub"
SPOKE_PROFILES=(dev staging prod)
ALL_PROFILES=("$HUB_PROFILE" "${SPOKE_PROFILES[@]}")

# Pinned. See docs/ for why these exact versions.
ARGOCD_VERSION="v3.5.1"
IMAGE_UPDATER_VERSION="v1.3.0"

ctx_for()   { echo "colima-$1"; }
# The lima user-v2 FQDN. This -- not the address colima writes into kubeconfig --
# is the only name another VM can reach. See docs/spike-01-vm-networking.md.
peer_fqdn_for() { echo "lima-colima-$1.internal"; }

# k3s listens on a random per-profile port, so never assume 6443.
api_port_for() {
  local ctx; ctx="$(ctx_for "$1")"
  kubectl config view -o jsonpath="{.clusters[?(@.name==\"${ctx}\")].cluster.server}" \
    | sed -E 's#.*:([0-9]+)$#\1#'
}

require_tools() {
  local missing=()
  for t in "$@"; do command -v "$t" >/dev/null 2>&1 || missing+=("$t"); done
  [ ${#missing[@]} -eq 0 ] || die "missing required tools: ${missing[*]}"
}

profile_running() {
  colima status --profile "$1" >/dev/null 2>&1
}
