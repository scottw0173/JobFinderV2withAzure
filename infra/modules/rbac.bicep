@description('Principal ID of the Job\'s user-assigned identity.')
param uamiPrincipalId string

@description('Resource ID of the Azure Container Registry.')
param acrId string

@description('Resource ID of the Key Vault.')
param keyVaultId string

@description('Resource ID of the storage account.')
param storageAccountId string

@description('Resource ID of the Cognitive Services / AI Foundry account.')
param openAiAccountIds array

@description('Name of the user-assigned managed identity used by GitHub Actions CI (created in ci-oidc.bicep). Looked up as existing to read its principalId for the AcrPush role assignment.')
param ciUamiName string = 'jf-dev-ci-uami'

@description('Name of the Container Apps Job.')
param jobName string

var acrPullRoleId = '7f951dda-4ed3-4680-a7ca-43fe172d538d'
var keyVaultSecretsUserRoleId = '4633458b-17de-408a-b874-0445c86b69e6'
var storageBlobDataReaderRoleId = '2a2b9908-6ea1-4ae2-8e65-a410df84e7d1'
var cognitiveServicesOpenAiUserRoleId = '5e0bd9bd-7b93-4f28-af87-19fc36ad61bd'

// Postgres intentionally has NO role assignment here - access is granted
// via the SQL-level administrators/pgaadauth_create_principal mechanism in
// postgres.bicep, per the hard rule's explicit callout that plain RBAC is
// insufficient for Postgres.

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: last(split(acrId, '/'))
}
resource ciUami 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = { name: ciUamiName }

resource job 'Microsoft.App/jobs@2024-03-01' existing = {
  name: jobName
}

resource jobWriter 'Microsoft.Authorization/roleDefinitions@2022-04-01' = {
  name: guid(job.id, 'ci-job-writer')
  properties: {
    roleName: 'JF CI Job Image Updater (${jobName})'
    assignableScopes: [ job.id ]
    permissions: [ { actions: [
      'Microsoft.App/jobs/read'
      'Microsoft.App/jobs/write'
    ] } ]
  }
}

resource jobWrite 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: job
  name: guid(job.id, ciUami.id, jobWriter.id)
  properties: {
    principalId: ciUami.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: jobWriter.id
  }
}

resource push 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: acr
  name: guid(acr.id, ciUami.id, '8311e382-0749-4cb8-b61a-304f252e45ec')
  properties: {
    principalId: ciUami.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '8311e382-0749-4cb8-b61a-304f252e45ec')
  }
}

resource acrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(acrId, uamiPrincipalId, acrPullRoleId)
  scope: acr
  properties: {
    principalId: uamiPrincipalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', acrPullRoleId)
  }
}

resource keyVault 'Microsoft.KeyVault/vaults@2023-07-01' existing = {
  name: last(split(keyVaultId, '/'))
}

resource keyVaultSecretsUser 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(keyVaultId, uamiPrincipalId, keyVaultSecretsUserRoleId)
  scope: keyVault
  properties: {
    principalId: uamiPrincipalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', keyVaultSecretsUserRoleId)
  }
}

resource storageAccount 'Microsoft.Storage/storageAccounts@2023-01-01' existing = {
  name: last(split(storageAccountId, '/'))
}

resource storageBlobDataReader 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(storageAccountId, uamiPrincipalId, storageBlobDataReaderRoleId)
  scope: storageAccount
  properties: {
    principalId: uamiPrincipalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', storageBlobDataReaderRoleId)
  }
}

resource openAiAccounts 'Microsoft.CognitiveServices/accounts@2025-06-01' existing = [
  for id in openAiAccountIds: {
    name: last(split(id, '/'))
  }
]

resource cognitiveServicesOpenAiUser 'Microsoft.Authorization/roleAssignments@2022-04-01' = [
  for (id, i) in openAiAccountIds: {
    name: guid(id, uamiPrincipalId, cognitiveServicesOpenAiUserRoleId)
    scope: openAiAccounts[i]
    properties: {
      principalId: uamiPrincipalId
      principalType: 'ServicePrincipal'
      roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', cognitiveServicesOpenAiUserRoleId)
    }
  }
]
