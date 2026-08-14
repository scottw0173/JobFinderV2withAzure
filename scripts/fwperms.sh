#!/usr/bin/env bash
#Use this script to give your current IP access to you
#AAD account through the firewall. May need to run every 
#Few days as the public IP will rotate routinely.

set -euo pipefail

#Resource group for group described in README. 
#Overridable if you use a different name for the resource group.
RG="${RG:-jobfinder-rg}"

# SERVER_NAME: the one Flexible Server in the RG. Scoped with -g so [0] can't
# resolve to a server in another resource group elsewhere in the subscription.
SERVER_NAME="${SERVER_NAME:-$(az postgres flexible-server list -g "$RG" \
  --query "[0].name" -o tsv)}"
: "${SERVER_NAME:?could not derive Flexible Server name from RG '$RG'}"

# IP: current public IPv4, fetched at runtime.
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

  echo "Successfully opened firewall for $IP on $SERVER_NAME"

