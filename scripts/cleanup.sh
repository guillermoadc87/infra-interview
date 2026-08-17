#!/usr/bin/env bash
# Tear down every colima profile this project creates.
# Usage: cleanup.sh [profile ...]     (default: all four)
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

PROFILES=("$@")
[ ${#PROFILES[@]} -gt 0 ] || PROFILES=("${ALL_PROFILES[@]}")

for p in "${PROFILES[@]}"; do
  info "deleting colima profile: $p"
  colima delete --profile "$p" --force 2>/dev/null || true
  ctx="$(ctx_for "$p")"
  kubectl config delete-context "$ctx" 2>/dev/null || true
  kubectl config delete-cluster "$ctx" 2>/dev/null || true
  kubectl config delete-user    "$ctx" 2>/dev/null || true
done

ok "done. To start fresh: ./scripts/setup.sh"
