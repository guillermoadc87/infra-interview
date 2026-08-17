#!/usr/bin/env bash
# DEPRECATED -- kept as break-glass only.
#
# Credentials now come from Vault via External Secrets: see
# gitops/platform/{external-secrets,vault-config} and each app's
# base/external-secret.yaml. A spoke needs NO manual secret.
#
# The only reason this still exists is recovery: if External Secrets or Vault is
# broken and you need the database up to debug something else, this puts a
# credential in place by hand. Doing so will FIGHT the ExternalSecret, which owns
# those Secrets (creationPolicy: Owner) and will overwrite them on its next
# refresh -- so delete the ExternalSecret first if you really mean it.
#
# Usage: seed-spoke-secrets.sh <env> [password]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

warn "DEPRECATED: credentials are materialised from Vault by External Secrets."
warn "This is break-glass only and will be overwritten by the ExternalSecret."
printf 'Continue? [y/N] ' >&2
read -r ans; [ "$ans" = "y" ] || die "aborted"

ENV_NAME="${1:-}"
[ -n "$ENV_NAME" ] || die "usage: $0 <env> [password]"
require_tools kubectl
CTX="$(ctx_for "$ENV_NAME")"
PW="${2:-$(openssl rand -base64 24 | tr -d '\n/+=' | cut -c1-24)}"

for ns in platform shop; do
  kubectl --context "$CTX" create namespace "$ns" --dry-run=client -o yaml \
    | kubectl --context "$CTX" apply -f - >/dev/null
  kubectl --context "$CTX" -n "$ns" create secret generic postgres-credentials \
    --from-literal=username=postgres --from-literal=password="$PW" \
    --dry-run=client -o yaml | kubectl --context "$CTX" apply -f - >/dev/null
  ok "postgres-credentials forced into ${ENV_NAME}/${ns}"
done
