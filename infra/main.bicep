targetScope = 'resourceGroup'

@description('Resource IDs of hand-created Foundry/OpenAI accounts (e.g. serverless-only models in other regions) that live outside this template but still need the app identity granted Cognitive Services OpenAI User. Must reside in this resource group. Leave empty if all models run on the Bicep-created account.')
param extraOpenAiAccountIds array = []

@description('Short name prefix driving all resource naming, e.g. "jf-dev".')
param namePrefix string

@description('Azure region for most resources. Must carry the target Azure OpenAI/Foundry model catalog - verify against the live catalog before deployment.')
param location string = resourceGroup().location

@description('Full container image reference to deploy. No CI/CD pushes it yet (out of scope for this step) - defaults to a tag under the provisioned ACR that must be pushed manually.')
param containerImage string = ''

@description('Entra object ID of the human/service principal to register as Postgres AAD administrator.')
param postgresAdminObjectId string

@description('Principal name (UPN or display name) matching postgresAdminObjectId.')
param postgresAdminPrincipalName string

@description('ACR SKU.')
param acrSku string = 'Basic'

@description('Postgres SKU name.')
param postgresSkuName string = 'Standard_B1ms'

@description('Postgres SKU tier.')
param postgresSkuTier string = 'Burstable'

@description('Azure OpenAI/Foundry account SKU.')
param openAiSkuName string = 'S0'

@description('JSON-encoded AZURE_MODELS override for the Container Apps Job - omit to use the Go code\'s defaultAzureModels.')
param azureModelsJson string = '[{"name":"Kimi-K2.5","deployment":"Kimi-K2.5","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":20000,"rpm":20,"wantLogprobs":true}]'

var uniqueSuffix = uniqueString(resourceGroup().id)

// ---- Identity ----

module jobIdentity 'modules/identity.bicep' = {
  name: 'jobIdentity'
  params: {
    name: '${namePrefix}-uami'
    location: location
  }
}

module pgScriptIdentity 'modules/identity.bicep' = {
  name: 'pgScriptIdentity'
  params: {
    name: '${namePrefix}-pgscript-uami'
    location: location
  }
}

// ---- Registry, logging, environment ----

module registry 'modules/registry.bicep' = {
  name: 'registry'
  params: {
    name: replace('${namePrefix}acr${uniqueSuffix}', '-', '')
    location: location
    skuName: acrSku
  }
}

module logAnalytics 'modules/logAnalytics.bicep' = {
  name: 'logAnalytics'
  params: {
    name: '${namePrefix}-law'
    location: location
  }
}

module containerAppsEnv 'modules/containerAppsEnv.bicep' = {
  name: 'containerAppsEnv'
  params: {
    name: '${namePrefix}-cae'
    location: location
    logAnalyticsWorkspaceName: '${namePrefix}-law'
  }
  dependsOn: [
    logAnalytics
  ]
}

// ---- Storage + Key Vault (future ConfigSource / Secrets backing - no consuming Go code yet) ----

module storage 'modules/storage.bicep' = {
  name: 'storage'
  params: {
    name: replace('${namePrefix}st${uniqueSuffix}', '-', '')
    location: location
  }
}

module keyVault 'modules/keyVault.bicep' = {
  name: 'keyVault'
  params: {
    name: '${namePrefix}-kv-${uniqueSuffix}'
    location: location
  }
}

// ---- Azure OpenAI / Foundry ----

module openai 'modules/openai.bicep' = {
  name: 'openai'
  params: {
    name: '${namePrefix}-ai-${uniqueSuffix}'
    location: location
    skuName: openAiSkuName
  }
}

// ---- Postgres ----

module postgres 'modules/postgres.bicep' = {
  name: 'postgres'
  params: {
    name: '${namePrefix}-pg-${uniqueSuffix}'
    location: location
    skuName: postgresSkuName
    skuTier: postgresSkuTier
    adminObjectId: postgresAdminObjectId
    adminPrincipalName: postgresAdminPrincipalName
    scriptIdentityObjectId: pgScriptIdentity.outputs.principalId
    scriptIdentityPrincipalName: pgScriptIdentity.outputs.name
    scriptIdentityResourceId: pgScriptIdentity.outputs.id
    appIdentityName: jobIdentity.outputs.name
  }
}

// ---- Container Apps Job ----

module containerAppsJob 'modules/containerAppsJob.bicep' = {
  name: 'containerAppsJob'
  params: {
    name: '${namePrefix}-job'
    location: location
    environmentId: containerAppsEnv.outputs.id
    uamiId: jobIdentity.outputs.id
    uamiClientId: jobIdentity.outputs.clientId
    acrLoginServer: registry.outputs.loginServer
    containerImage: empty(containerImage) ? '${registry.outputs.loginServer}/jobfinder:latest' : containerImage
    openAiEndpoint: openai.outputs.endpoint
    storageAccountName: storage.outputs.name
    azureModelsJson: azureModelsJson
    postgresFqdn: postgres.outputs.fqdn
    postgresDatabaseName: postgres.outputs.databaseName
    postgresAppPrincipalName: jobIdentity.outputs.name
  }
}

// ---- RBAC ----

module rbac 'modules/rbac.bicep' = {
  name: 'rbac'
  params: {
    uamiPrincipalId: jobIdentity.outputs.principalId
    acrId: registry.outputs.id
    keyVaultId: keyVault.outputs.id
    storageAccountId: storage.outputs.id
    openAiAccountIds: union([openai.outputs.id], extraOpenAiAccountIds)
  }
}

// ---- Outputs ----

output acrLoginServer string = registry.outputs.loginServer
output postgresFqdn string = postgres.outputs.fqdn
output postgresDatabaseName string = postgres.outputs.databaseName
output openAiEndpoint string = openai.outputs.endpoint
output keyVaultUri string = keyVault.outputs.uri
output storageAccountName string = storage.outputs.name
output storageContainerName string = storage.outputs.containerName
output uamiPrincipalId string = jobIdentity.outputs.principalId
output uamiClientId string = jobIdentity.outputs.clientId
output containerAppsJobName string = containerAppsJob.outputs.name
