#!/bin/bash
# Creates the per-service databases on first initialisation of an empty data
# directory. Mounted at /docker-entrypoint-initdb.d by the Deployment.
#
# Migrated from charts/postgres/templates/configmap.yaml, where this was built by
# `range .Values.initDatabases`. That indirection produced a fixed two-database
# script and bought nothing, so it is now a real file -- which also means it is
# lintable, diffable, and executable locally.
#
# NOTE: this script is delivered via configMapGenerator, so kustomize appends a
# content hash to the ConfigMap name and rewrites the Deployment's volume
# reference. Editing this file therefore rolls the pod automatically. The Helm
# version silently did not.
set -e

for db in orders inventory; do
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" <<-EOSQL
    SELECT 'CREATE DATABASE $db' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '$db')\gexec
EOSQL
done

# Group roles that OWN the schema, so dynamic credentials work.
#
# Vault's database secrets engine issues a brand new PostgreSQL role per
# application per lease. Without a stable owner, the first dynamic user would
# create the tables, own them, and then be dropped when its lease expired --
# taking the tables with it, or leaving the next user unable to touch them.
#
# Instead each dynamic user is created IN one of these group roles, and Vault's
# revocation statement reassigns anything it owned to the group before dropping
# it. The group is permanent; the logins are disposable.
for pair in "orders:app_orders" "inventory:app_inventory"; do
  db="${pair%%:*}"; grp="${pair##*:}"
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$db" <<-EOSQL
    DO \$\$ BEGIN
      IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$grp') THEN
        CREATE ROLE $grp NOLOGIN;
      END IF;
    END \$\$;
    GRANT ALL ON SCHEMA public TO $grp;
    GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO $grp;
    GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO $grp;
    -- Anything a future dynamic user creates is usable by the whole group, so
    -- the next lease can read the previous lease's tables.
    ALTER DEFAULT PRIVILEGES IN SCHEMA public
      GRANT ALL ON TABLES TO $grp;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public
      GRANT ALL ON SEQUENCES TO $grp;
EOSQL
done
