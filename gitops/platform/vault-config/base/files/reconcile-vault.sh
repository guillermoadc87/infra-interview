#!/bin/sh
# Bring Vault to the state External Secrets needs. Fully idempotent: safe to run
# on a schedule forever, which is what the CronJob does.
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
# must notice Vault is sealed" into "the platform repairs itself".
#
# Deliberately uses ONLY curl plus shell builtins -- no vault CLI, no kubectl, no
# jq. That is what lets it run on curlimages/curl as a NON-ROOT user with a
# read-only root filesystem: installing packages at runtime (apk add) requires
# root, which is not a trade worth making for a JSON parser. Every value handled
# here is base64 or alphanumeric, so sed-based parsing is safe.
set -eu

VAULT_ADDR="${VAULT_ADDR:-http://vault.vault.svc.cluster.local:8200}"
NS="${POD_NAMESPACE:-vault}"
KEYS_SECRET=vault-unseal-keys

SA=/var/run/secrets/kubernetes.io/serviceaccount
KUBE_API="https://kubernetes.default.svc"
KUBE_TOKEN="$(cat $SA/token)"
KUBE_CA="$SA/ca.crt"

log() { echo "[vault-config] $*"; }

# Pull a scalar out of a JSON body without jq. Values here are booleans, base64
# strings or alphanumerics -- never anything needing real parsing.
jval()  { sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" <<EOF | head -1
$1
EOF
}
jbool() { sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\([a-z]*\).*/\1/p" <<EOF | head -1
$1
EOF
}

v_get()   { curl -sS --max-time 15 -H "X-Vault-Token: ${VAULT_TOKEN:-}" "$VAULT_ADDR/v1/$1"; }
v_write() { curl -sS --max-time 15 -X "${3:-POST}" -H "X-Vault-Token: ${VAULT_TOKEN:-}" \
              -d "$2" "$VAULT_ADDR/v1/$1"; }
k_get()   { curl -sS --max-time 15 --cacert "$KUBE_CA" \
              -H "Authorization: Bearer $KUBE_TOKEN" "$KUBE_API/$1"; }
k_post()  { curl -sS --max-time 15 --cacert "$KUBE_CA" \
              -H "Authorization: Bearer $KUBE_TOKEN" -H 'Content-Type: application/json' \
              -X POST -d "$2" "$KUBE_API/$1"; }

# ------------------------------------------------------------------ wait ------
# The Service can exist before Vault answers, so retry rather than fail: a slow
# first start is not an error.
i=0
while :; do
  STATUS="$(v_get sys/seal-status 2>/dev/null || true)"
  case "$STATUS" in *'"sealed"'*) break ;; esac
  i=$((i+1))
  [ "$i" -ge 40 ] && { log "vault never answered at $VAULT_ADDR"; exit 1; }
  log "waiting for vault ($i)"; sleep 5
done

INITIALIZED="$(jbool "$STATUS" initialized)"
SEALED="$(jbool "$STATUS" sealed)"
log "initialized=$INITIALIZED sealed=$SEALED"

# ------------------------------------------------------------------ init ------
if [ "$INITIALIZED" = "false" ]; then
  log "initialising vault"
  # 1 share / threshold 1 because this is a single-node local Vault. A real
  # deployment uses 5/3 with shares held by separate people.
  OUT="$(v_write sys/init '{"secret_shares":1,"secret_threshold":1}' PUT)"
  UNSEAL_KEY="$(printf '%s' "$OUT" | sed -n 's/.*"keys_base64":\[\"\([^"]*\)\".*/\1/p' | head -1)"
  ROOT_TOKEN="$(jval "$OUT" root_token)"
  [ -n "$UNSEAL_KEY" ] && [ -n "$ROOT_TOKEN" ] || { log "init failed: $OUT"; exit 1; }

  # Persist BEFORE unsealing: crashing here without the key would leave the PVC
  # permanently unrecoverable.
  BODY="$(printf '{"apiVersion":"v1","kind":"Secret","metadata":{"name":"%s"},"type":"Opaque","stringData":{"unseal-key":"%s","root-token":"%s"}}' \
    "$KEYS_SECRET" "$UNSEAL_KEY" "$ROOT_TOKEN")"
  k_post "api/v1/namespaces/$NS/secrets" "$BODY" >/dev/null
  log "stored unseal key and root token in secret/$KEYS_SECRET"
fi

# ---------------------------------------------------------------- unseal ------
KEYS_JSON="$(k_get "api/v1/namespaces/$NS/secrets/$KEYS_SECRET")"
UNSEAL_KEY="$(jval "$KEYS_JSON" unseal-key | base64 -d 2>/dev/null || true)"
VAULT_TOKEN="$(jval "$KEYS_JSON" root-token | base64 -d 2>/dev/null || true)"
export VAULT_TOKEN
[ -n "$VAULT_TOKEN" ] || { log "no root token in secret/$KEYS_SECRET; cannot configure"; exit 1; }

if [ "$(jbool "$(v_get sys/seal-status)" sealed)" = "true" ]; then
  log "sealed -- unsealing"
  v_write sys/unseal "$(printf '{"key":"%s"}' "$UNSEAL_KEY")" PUT >/dev/null
  [ "$(jbool "$(v_get sys/seal-status)" sealed)" = "false" ] || { log "unseal failed"; exit 1; }
  log "unsealed"
fi

# -------------------------------------------------------------- kv engine -----
# Dev mode mounted secret/ automatically; standalone does not.
case "$(v_get sys/mounts 2>/dev/null || true)" in
  *'"secret/"'*) : ;;
  *) log "enabling kv-v2 at secret/"
     v_write sys/mounts/secret '{"type":"kv","options":{"version":"2"}}' >/dev/null ;;
esac

# --------------------------------------------------- kubernetes auth ---------
# This is what removes every static credential: External Secrets presents its own
# ServiceAccount JWT and Vault verifies it against the cluster API.
case "$(v_get sys/auth 2>/dev/null || true)" in
  *'"kubernetes/"'*) : ;;
  *) log "enabling kubernetes auth"
     v_write sys/auth/kubernetes '{"type":"kubernetes"}' >/dev/null ;;
esac

v_write auth/kubernetes/config \
  '{"kubernetes_host":"https://kubernetes.default.svc:443"}' >/dev/null
log "configured auth/kubernetes"

# Least privilege: read-only, and only the single path ESO needs.
v_write sys/policies/acl/eso-read \
  '{"policy":"path \"secret/data/postgres\" { capabilities = [\"read\"] }\npath \"secret/metadata/postgres\" { capabilities = [\"read\",\"list\"] }"}' \
  PUT >/dev/null
log "wrote policy eso-read"

v_write auth/kubernetes/role/eso \
  '{"bound_service_account_names":"external-secrets","bound_service_account_namespaces":"external-secrets","token_policies":"eso-read","token_ttl":"1h"}' \
  >/dev/null
log "wrote role eso (external-secrets/external-secrets -> eso-read)"

# ------------------------------------------------------------------ seed ------
# Only generate a password if the path is empty, so re-running never rotates the
# credential out from under a running database.
case "$(v_get secret/data/postgres 2>/dev/null || true)" in
  *'"password"'*) log "secret/postgres already populated -- leaving it alone" ;;
  *) PW="$(tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 24)"
     v_write secret/data/postgres \
       "$(printf '{"data":{"username":"postgres","password":"%s"}}' "$PW")" >/dev/null
     log "seeded secret/postgres with a generated password" ;;
esac

# -------------------------------------------------- database engine ----------
# DYNAMIC CREDENTIALS.
#
# Everything above hands out one shared, long-lived password. This section makes
# Vault issue a BRAND NEW PostgreSQL role per application per lease, with a TTL,
# and revoke it on expiry. There is then no shared database password to rotate,
# which removes the ordering problem entirely: rotation stops being a procedure
# and becomes the normal state of affairs.
#
# The static secret/postgres credential above does NOT go away -- it is how
# Postgres bootstraps itself (POSTGRES_PASSWORD) and how Vault authenticates as
# admin to mint the dynamic roles. Something has to initialise the database.
case "$(v_get sys/mounts 2>/dev/null || true)" in
  *'"database/"'*) : ;;
  *) log "enabling the database secrets engine"
     v_write sys/mounts/database '{"type":"database"}' >/dev/null ;;
esac

# Vault connects as the admin whose password lives in secret/postgres.
ADMIN_PW="$(v_get secret/data/postgres | sed -n 's/.*"password":"\([^"]*\)".*/\1/p' | head -1)"
[ -n "$ADMIN_PW" ] || { log "cannot read the admin password from secret/postgres"; exit 1; }

PGHOST="postgres.platform.svc.cluster.local:5432"

# One connection per database: the creation statements GRANT on `schema public`,
# which only exists inside a specific database, so a single connection could not
# serve both.
for pair in "orders:api-service:app_orders" "inventory:inventory-service:app_inventory"; do
  DB="${pair%%:*}"; REST="${pair#*:}"; APP="${REST%%:*}"; GRP="${REST##*:}"

  v_write "database/config/postgres-${DB}" "$(printf '{"plugin_name":"postgresql-database-plugin","allowed_roles":"%s","connection_url":"postgresql://{{username}}:{{password}}@%s/%s?sslmode=disable","username":"postgres","password":"%s","password_authentication":"scram-sha-256"}' \
    "$APP" "$PGHOST" "$DB" "$ADMIN_PW")" >/dev/null

  # The dynamic role is created IN the group role that owns the schema, and its
  # revocation REASSIGNS anything it made to that group before dropping it --
  # otherwise an expiring lease would take the application's tables with it.
  v_write "database/roles/${APP}" "$(printf '{"db_name":"postgres-%s","creation_statements":"CREATE ROLE \"{{name}}\" WITH LOGIN PASSWORD '"'"'{{password}}'"'"' VALID UNTIL '"'"'{{expiration}}'"'"' IN ROLE %s INHERIT; GRANT ALL ON SCHEMA public TO \"{{name}}\";","revocation_statements":"REASSIGN OWNED BY \"{{name}}\" TO %s; DROP OWNED BY \"{{name}}\"; DROP ROLE IF EXISTS \"{{name}}\";","default_ttl":"1h","max_ttl":"24h"}' \
    "$DB" "$GRP" "$GRP")" >/dev/null

  # Least privilege per application: api-service can read ONLY its own
  # credential path. It cannot obtain inventory-service's database access.
  v_write "sys/policies/acl/${APP}-db" \
    "$(printf '{"policy":"path \\"database/creds/%s\\" { capabilities = [\\"read\\"] }"}' "$APP")" PUT >/dev/null

  # Each application authenticates as ITSELF, with its own ServiceAccount --
  # not through the External Secrets controller's identity.
  v_write "auth/kubernetes/role/${APP}" "$(printf '{"bound_service_account_names":"%s-vault","bound_service_account_namespaces":"shop","token_policies":"%s-db","token_ttl":"1h"}' \
    "$APP" "$APP")" >/dev/null

  log "database role ${APP} -> postgres-${DB} (owner ${GRP}, ttl 1h)"
done

log "done"
