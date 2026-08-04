#!/usr/bin/env bash
# deploy-ci-oidc.sh
set -euo pipefail

RG="${RG:-jobfinder-rg}"

# Parse owner/repo from origin (handles both SSH and HTTPS remotes)
origin=$(git remote get-url origin)
repo_path=${origin#*github.com[:/]}   # strip through github.com: or github.com/
repo_path=${repo_path%.git}           # strip trailing .git
owner=${repo_path%%/*}
repo=${repo_path#*/}
owner_id=$(gh api "repos/${owner}/${repo}" --jq .owner.id)
repo_id=$(gh api "repos/${owner}/${repo}" --jq .id)

echo "Deploying CI OIDC for ${owner}/${repo} into ${RG}"

az deployment group create \
  -g "$RG" \
  -f infra/ci-oidc.bicep \
  -p githubOwner="$owner" githubRepo="$repo" \
     githubOwnerId="$owner_id" githubRepoId="$repo_id"