#!/usr/bin/env bash
# Create the one out-of-band Secret a spoke needs before it can converge.
#
# This is the single manual input that is NOT in git, because the repository is
# public. It is needed in BOTH namespaces that consume it -- `platform`, where
# Postgres initialises itself, and `shop`, where the applications connect --
# because Kubernetes Secrets are namespace-scoped.
#
# In a real deployment this script does not exist: External Secrets syncs one
# backend key into each namespace instead. That is the intended replacement, and
# the reason this file is small and boring rather than clever.
#
# Usage: seed-spoke-secrets.sh <env> [password]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

ENV_NAME="${1:-}"
[ -n "$ENV_NAME" ] || die "usage: $0 <env> [password]"
require_tools kubectl

CTX="$(ctx_for "$ENV_NAME")"
# A generated password by default. Pass one explicitly only when you need to
# reach the database yourself.
PW="${2:-$(openssl rand -base64 24 | tr -d '\n/+=' | cut -c1-24)}"

for ns in platform shop; do
  kubectl --context "$CTX" create namespace "$ns" --dry-run=client -o yaml \
    | kubectl --context "$CTX" apply -f - >/dev/null
  kubectl --context "$CTX" -n "$ns" create secret generic postgres-credentials \
    --from-literal=username=postgres \
    --from-literal=password="$PW" \
    --dry-run=client -o yaml | kubectl --context "$CTX" apply -f - >/dev/null
  ok "postgres-credentials present in ${ENV_NAME}/${ns}"
done

echo
echo "  Both namespaces share one password, which is what Postgres and the apps"
echo "  agreeing on a credential requires. Rotating means updating both."
