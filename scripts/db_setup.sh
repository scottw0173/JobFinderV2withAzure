#!/usr/bin/env bash
# Azure app-DB bootstrap. Run ONCE after `az deployment group create` succeeds
# (that registers the runtime identity as a Postgres role). Run as yourself
# (human AAD admin). Opens a Postgres firewall exception for your current
# public IP, then applies schema + grants. Idempotent.
#
# Values are derived from the resource group by default (same [0]/signed-in-user
# pattern as bootstrap.sh and kvperm.sh); any can still be overridden by env.
set -euo pipefail

RG="${RG:-jobfinder-rg}"
APP_DB="${APP_DB:-jobfinder}"

# SERVER_NAME: the one Flexible Server in the RG. Scoped with -g so [0] can't
# resolve to a server in another resource group elsewhere in the subscription.
SERVER_NAME="${SERVER_NAME:-$(az postgres flexible-server list -g "$RG" \
  --query "[0].name" -o tsv)}"
: "${SERVER_NAME:?could not derive Flexible Server name from RG '$RG'}"

# IP: current public IPv4, fetched at runtime (nothing sensitive is committed).
# Overridable if you're behind a proxy or want to authorize a CIDR by hand.
IP="${IP:-$(curl -s https://api.ipify.org)}"
: "${IP:?could not determine public IP (is the network up?)}"

# Open the admin port to just this IP. create is an upsert on the rule name, so
# re-running after your IP changes updates the rule rather than erroring.
echo "Opening firewall for $IP on $SERVER_NAME ..."
az postgres flexible-server firewall-rule create \
  -g "$RG" \
  --server-name "$SERVER_NAME" \
  --name allow-my-ip \
  --start-ip-address "$IP" \
  --end-ip-address "$IP" >/dev/null

# SERVER_FQDN: reuse the name we already resolved above.
SERVER_FQDN="${SERVER_FQDN:-$(az postgres flexible-server show -g "$RG" \
  -n "$SERVER_NAME" --query fullyQualifiedDomainName -o tsv)}"

# ADMIN_UPN: derive from the signed-in user, exactly as bootstrap.sh does when
# it registers the Postgres AAD admin. Guest (B2B) accounts return the #EXT#
# form here automatically, so this stays in lockstep with the registered role.
ADMIN_UPN="${ADMIN_UPN:-$(az ad signed-in-user show \
  --query userPrincipalName -o tsv)}"

# RUNTIME_UAMI: the runtime identity role name (= UAMI name). NOT a bare [0] —
# the RG also holds <prefix>-pgscript-uami, and granting DB access to the wrong
# identity would be silent. Match the -uami suffix and exclude the pgscript one.
RUNTIME_UAMI="${RUNTIME_UAMI:-$(az identity list -g "$RG" \
  --query "[?ends_with(name, '-uami') && !contains(name, 'pgscript')].name | [0]" -o tsv)}"

# Fail loud if any derivation came back empty rather than build a garbage DSN
# and point psql at the wrong place.
: "${SERVER_FQDN:?could not derive Flexible Server FQDN from RG '$RG'}"
: "${ADMIN_UPN:?could not derive signed-in-user UPN (are you logged in with 'az login'?)}"
: "${RUNTIME_UAMI:?could not derive runtime UAMI from RG '$RG'}"

echo "Server:  $SERVER_FQDN"
echo "DB:      $APP_DB"
echo "Admin:   $ADMIN_UPN"
echo "Runtime: $RUNTIME_UAMI"

export PGPASSWORD="$(az account get-access-token --resource-type oss-rdbms --query accessToken -o tsv)"
DSN="host=${SERVER_FQDN} port=5432 dbname=${APP_DB} user=${ADMIN_UPN} sslmode=require"

psql "$DSN" -v ON_ERROR_STOP=1 -f db/schema.sql
psql "$DSN" -v ON_ERROR_STOP=1 -v uami="$RUNTIME_UAMI" -f db/grants.sql
echo "DB bootstrap complete."