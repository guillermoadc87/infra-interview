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
