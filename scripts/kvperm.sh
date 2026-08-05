#!/usr/bin/env bash
#Use this script to give the give permissions for 
#the user to add values to the keyVault already
#created by the RBAC. Run and wait 2-5 before setting secrets

set -euo pipefail

RG="${RG:-jobfinder-rg}"
ADMIN_OID=$(az ad signed-in-user show --query id -o tsv)
VAULT=$(az keyvault list -g "$RG" --query "[0].name" -o tsv)

az role assignment create \
  --role "Key Vault Secrets Officer" \
  --assignee-object-id "$ADMIN_OID" \
  --assignee-principal-type User \
  --scope $(az keyvault show -n "$VAULT" --query id -o tsv)

echo "permission successfully set for $VAULT"