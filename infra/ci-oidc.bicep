// ci-oidc.bicep
param location string = resourceGroup().location
param githubOwner string          // These values will be set by a
param githubRepo string           // manual deployment of oidc.sh
param githubRef string = 'refs/heads/main'
@description('GitHub org/user numeric ID (immutable). Derived in oidc.sh via gh api.')
param githubOwnerId string
@description('GitHub repo numeric ID (immutable). Derived in oidc.sh via gh api.')
param githubRepoId string

resource ciUami 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: 'jf-dev-ci-uami'
  location: location
}
resource fed 'Microsoft.ManagedIdentity/userAssignedIdentities/federatedIdentityCredentials@2023-01-31' = {
  parent: ciUami
  name: 'github-actions-main'
  properties: {
    issuer: 'https://token.actions.githubusercontent.com'
    subject: 'repo:${githubOwner}@${githubOwnerId}/${githubRepo}@${githubRepoId}:ref:${githubRef}'
    audiences: [ 'api://AzureADTokenExchange' ]
  }
}
output ciUamiClientId string = ciUami.properties.clientId
output ciUamiPrincipalId string = ciUami.properties.principalId
