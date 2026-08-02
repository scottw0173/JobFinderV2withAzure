#!/usr/bin/env bash
#run this script to deploy the bicep configuration
#then build and push the docker image tagged to the latest
#SHA from github and then update the bicep to point at the image

set -euo pipefail

RG="${RG:-jobfinder-rg}"
ADMIN_OID=$(az ad signed-in-user show --query id -o tsv)
ADMIN_UPN=$(az ad signed-in-user show --query userPrincipalName -o tsv)
CONTRIBUTOR_ID=$(printf '%s' "$ADMIN_UPN" | sha256sum | cut -c1-12)
#Deploying the bicep initially to ACR. 
az deployment group create -g "$RG" -f ./infra/main.bicep -p ./infra/main.bicepparam -p postgresAdminObjectId="$ADMIN_OID" -p postgresAdminPrincipalName="$ADMIN_UPN" -p contributorId="$CONTRIBUTOR_ID"

ACR_LOGIN=$(az acr list -g "$RG" --query "[0].loginServer" -o tsv)
ACR=$(az acr list -g "$RG" --query "[0].name" -o tsv)
TAG=$(git rev-parse --short HEAD) #currently set to the shortened SHA of the last git commit. Probably need to adjust in final product
REPO_ROOT=$(git rev-parse --show-toplevel)

az acr login -n "$ACR"
docker build --platform linux/amd64 -t "$ACR_LOGIN/jobfinder:$TAG" "$REPO_ROOT"
docker push "$ACR_LOGIN/jobfinder:$TAG"

JOB_NAME=$(az containerapp job list -g "$RG" --query "[0].name" -o tsv)

az containerapp job update -n "$JOB_NAME" -g "$RG" --image "$ACR_LOGIN/jobfinder:$TAG"
