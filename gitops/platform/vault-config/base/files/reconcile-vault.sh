#!/bin/sh
# Bring Vault to the state External Secrets needs. Fully idempotent: safe to run
# on a schedule forever, which is exactly what the CronJob does.
#
# One code path covers four situations:
#   1. brand new Vault -> initialise, persist keys, unseal, configure, seed
#   2. restarted Vault -> already initialised but SEALED -> unseal
#   3. config drift    -> re-assert kv mount, auth method, policy, role
#   4. nothing to do   -> exits 0
#
# WHY THIS EXISTS: with file storage, Vault comes back SEALED after any restart
# and serves nothing until unsealed. Argo CD cannot detect that -- the Kubernetes
# resources are unchanged, so selfHeal has nothing to correct. This turns "a human
# must notice and re-seed Vault" into "the platform repairs itself".
#
# Uses the Vault and Kubernetes HTTP APIs directly rather than the vault/kubectl
# binaries, so the job runs on a plain alpine image with no large downloads on
# every scheduled run.
set -eu

VAULT_ADDR="${VAULT_ADDR:-http://vault.vault.svc.cluster.local:8200}"
NS="${POD_NAMESPACE:-vault}"
KEYS_SECRET=vault-unseal-keys

SA=/var/run/secrets/kubernetes.io/serviceaccount
KUBE_API="https://kubernetes.default.svc"
KUBE_TOKEN="$(cat $SA/token)"
KUBE_CA="$SA/ca.crt"

log() { echo "[vault-config] $*"; }
v_get() { curl -sS --max-time 15 "$VAULT_ADDR/v1/$1"; }
# $1 path, $2 body, $3 (optional) method
v_write() {
  curl -sS --max-time 15 -X "${3:-POST}" \
    -H "X-Vault-Token: ${VAULT_TOKEN:-}" \
    -d "$2" "$VAULT_ADDR/v1/$1"
}
k_get() {
  curl -sS --max-time 15 --cacert "$KUBE_CA" \
    -H "Authorization: Bearer $KUBE_TOKEN" "$KUBE_API/$1"
}
k_post() {
  curl -sS --max-time 15 --cacert "$KUBE_CA" \
    -H "Authorization: Bearer $KUBE_TOKEN" -H 'Content-Type: application/json' \
    -X POST -d "$2" "$KUBE_API/$1"
}

# ------------------------------------------------------------------ wait ------
# The Service can exist before Vault answers, so retry instead of failing: a slow
# first start is not an error.
i=0
until v_get sys/seal-status | grep -q '"sealed"'; do
  i=$((i+1))
  [ "$i" -ge 60 ] && { log "vault never answered at $VAULT_ADDR"; exit 1; }
  log "waiting for vault ($i)"; sleep 5
done

STATUS="$(v_get sys/seal-status)"
INITIALIZED="$(echo "$STATUS" | jq -r '.initialized')"
SEALED="$(echo "$STATUS" | jq -r '.sealed')"
log "initialized=$INITIALIZED sealed=$SEALED"

# ------------------------------------------------------------------ init ------
if [ "$INITIALIZED" = "false" ]; then
  log "initialising vault"
  # 1 share / threshold 1 because this is a single-node local Vault. A real
  # deployment uses 5/3 with shares held by separate people.
  OUT="$(v_write sys/init '{"secret_shares":1,"secret_threshold":1}' PUT)"
  UNSEAL_KEY="$(echo "$OUT" | jq -r '.keys_base64[0]')"
  ROOT_TOKEN="$(echo "$OUT" | jq -r '.root_token')"
  [ -n "$UNSEAL_KEY" ] && [ "$UNSEAL_KEY" != "null" ] || { log "init failed: $OUT"; exit 1; }

  # Persist BEFORE unsealing: crashing here without the key would leave the PVC
  # permanently unrecoverable.
  BODY="$(jq -nc --arg n "$KEYS_SECRET" --arg u "$UNSEAL_KEY" --arg r "$ROOT_TOKEN" \
    '{apiVersion:"v1",kind:"Secret",metadata:{name:$n},type:"Opaque",
      stringData:{"unseal-key":$u,"root-token":$r}}')"
  k_post "api/v1/namespaces/$NS/secrets" "$BODY" >/dev/null
  log "stored unseal key and root token in secret/$KEYS_SECRET"
fi

# ---------------------------------------------------------------- unseal ------
KEYS_JSON="$(k_get "api/v1/namespaces/$NS/secrets/$KEYS_SECRET")"
UNSEAL_KEY="$(echo "$KEYS_JSON" | jq -r '.data["unseal-key"] // empty' | base64 -d 2>/dev/null || true)"
VAULT_TOKEN="$(echo "$KEYS_JSON" | jq -r '.data["root-token"] // empty' | base64 -d 2>/dev/null || true)"
export VAULT_TOKEN
[ -n "$VAULT_TOKEN" ] || { log "no root token in secret/$KEYS_SECRET; cannot configure"; exit 1; }

if [ "$(v_get sys/seal-status | jq -r '.sealed')" = "true" ]; then
  log "sealed -- unsealing"
  v_write sys/unseal "$(jq -nc --arg k "$UNSEAL_KEY" '{key:$k}')" PUT >/dev/null
  [ "$(v_get sys/seal-status | jq -r '.sealed')" = "false" ] || { log "unseal failed"; exit 1; }
  log "unsealed"
fi

# -------------------------------------------------------------- kv engine -----
# Dev mode mounted secret/ automatically; standalone does not.
if ! v_write sys/mounts '' GET 2>/dev/null | jq -e '."secret/"' >/dev/null 2>&1; then
  log "enabling kv-v2 at secret/"
  v_write sys/mounts/secret '{"type":"kv","options":{"version":"2"}}' >/dev/null
fi

# --------------------------------------------------- kubernetes auth ---------
# This is what removes every static credential: External Secrets presents its
# ServiceAccount JWT and Vault verifies it against the cluster's API.
if ! v_write sys/auth '' GET 2>/dev/null | jq -e '."kubernetes/"' >/dev/null 2>&1; then
  log "enabling kubernetes auth"
  v_write sys/auth/kubernetes '{"type":"kubernetes"}' >/dev/null
fi

v_write auth/kubernetes/config \
  '{"kubernetes_host":"https://kubernetes.default.svc:443"}' >/dev/null
log "configured auth/kubernetes"

# Least privilege: read-only, and only the single path ESO needs.
POLICY='path "secret/data/postgres" { capabilities = ["read"] }
path "secret/metadata/postgres" { capabilities = ["read","list"] }'
v_write sys/policies/acl/eso-read "$(jq -nc --arg p "$POLICY" '{policy:$p}')" PUT >/dev/null
log "wrote policy eso-read"

v_write auth/kubernetes/role/eso \
  '{"bound_service_account_names":"external-secrets",
    "bound_service_account_namespaces":"external-secrets",
    "token_policies":"eso-read","token_ttl":"1h"}' >/dev/null
log "wrote role eso (external-secrets/external-secrets -> eso-read)"

# ------------------------------------------------------------------ seed ------
# Only generate a password if the path is empty, so re-running never rotates the
# credential out from under a running database.
if v_get secret/data/postgres 2>/dev/null | jq -e '.data.data.password' >/dev/null 2>&1; then
  log "secret/postgres already populated -- leaving it alone"
else
  PW="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 24)"
  v_write secret/data/postgres \
    "$(jq -nc --arg p "$PW" '{data:{username:"postgres",password:$p}}')" >/dev/null
  log "seeded secret/postgres with a generated password"
fi

log "done"
