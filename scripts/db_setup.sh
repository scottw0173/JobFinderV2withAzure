#!/usr/bin/env bash
# Azure app-DB bootstrap. Run ONCE after `az deployment group create` succeeds
# (that registers the runtime identity as a Postgres role). Run as yourself
# (human AAD admin), with your client IP on the server firewall. Idempotent.
set -euo pipefail

SERVER_FQDN="${SERVER_FQDN:?Flexible Server FQDN}"
APP_DB="${APP_DB:-jobfinder}"
ADMIN_UPN="${ADMIN_UPN:?your AAD admin UPN (the #EXT# guest form)}"
RUNTIME_UAMI="${RUNTIME_UAMI:?runtime identity role, e.g. jf-dev-uami}"

export PGPASSWORD="$(az account get-access-token --resource-type oss-rdbms --query accessToken -o tsv)"
DSN="host=${SERVER_FQDN} port=5432 dbname=${APP_DB} user=${ADMIN_UPN} sslmode=require"

psql "$DSN" -v ON_ERROR_STOP=1 -f db/schema.sql
psql "$DSN" -v ON_ERROR_STOP=1 -v uami="$RUNTIME_UAMI" -f db/grants.sql
echo "DB bootstrap complete."