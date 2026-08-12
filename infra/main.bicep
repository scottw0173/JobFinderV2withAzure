targetScope = 'resourceGroup'

@description('Resource IDs of hand-created Foundry/OpenAI accounts (e.g. serverless-only models in other regions) that live outside this template but still need the app identity granted Cognitive Services OpenAI User. Must reside in this resource group. Leave empty if all models run on the Bicep-created account.')
param extraOpenAiAccountIds array = []

@description('Short name prefix driving all resource naming, e.g. "jf-dev".')
param namePrefix string

@description('Azure region for most resources. Must carry the target Azure OpenAI/Foundry model catalog - verify against the live catalog before deployment.')
param location string = resourceGroup().location

@description('Full pinned image reference, digest-preferred: <registry>/jobfinder@sha256:<digest>')
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

@description('Two-tier deploy gate: false deploys/lists only the screener; true adds modelDeployments (tier-1 graders) to both the Foundry account and AZURE_MODELS.')
param enableFullPanel bool = false

@description('Tier-0 screening model - single source of truth for both the openai module (ARM: name/model/version/capacity) and AZURE_MODELS (runtime: name/deployment/protocol/baseURL/authScope/tpm/rpm/wantLogprobs). No default: version/capacity need live-catalog verification, supplied in main.bicepparam.')
param screenerModel object

@description('Tier-1 grader models - same dual-shape-object contract as screenerModel. Defaults mirror the already-deployed panel (CLAUDE.md §12).')
param modelDeployments array = [
  { name: 'gpt-5.4-mini',  model: 'gpt-5.4-mini',  version: '2026-03-17', capacity: 500,  deployment: 'gpt-5.4-mini',  protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 200000,  rpm: 1000, wantLogprobs: true }
  { name: 'gpt-5.3-codex', model: 'gpt-5.3-codex', version: '2026-02-24', capacity: 500,  deployment: 'gpt-5.3-codex', protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 500000,  rpm: 5000, wantLogprobs: false }
  { name: 'gpt-5.4-nano',  model: 'gpt-5.4-nano',  version: '2026-03-17', capacity: 2500, deployment: 'gpt-5.4-nano',  protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 2500000, rpm: 2500, wantLogprobs: true }
  { name: 'gpt-5.4',       model: 'gpt-5.4',       version: '2026-03-05', capacity: 500,  deployment: 'gpt-5.4',       protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 500000,  rpm: 5000, wantLogprobs: true }
  { name: 'gpt-5.5',       model: 'gpt-5.5',       version: '2026-04-24', capacity: 500,  deployment: 'gpt-5.5',       protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 500000,  rpm: 500,  wantLogprobs: false }
  { name: 'gpt-5.6-sol',   model: 'gpt-5.6-sol',   version: '2026-07-09', capacity: 500,  deployment: 'gpt-5.6-sol',   protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 500000,  rpm: 500,  wantLogprobs: false }
  { name: 'gpt-5.6-luna',  model: 'gpt-5.6-luna',  version: '2026-07-09', capacity: 500,  deployment: 'gpt-5.6-luna',  protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 500000,  rpm: 500,  wantLogprobs: false }
  { name: 'gpt-5.6-terra', model: 'gpt-5.6-terra', version: '2026-07-09', capacity: 500,  deployment: 'gpt-5.6-terra', protocol: 'openai', baseURL: 'https://jobfinderv2-resource.services.ai.azure.com/openai/v1', authScope: 'https://ai.azure.com/.default', tpm: 500000,  rpm: 500,  wantLogprobs: false }
]

@description('Deploy-time contributor identity token; injected by bootstrap.sh')
param contributorId string = 'UNSET'

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
    enableFullPanel: enableFullPanel
    screenerModel: screenerModel
    modelDeployments: modelDeployments
  }
}

// Screener always present; graders (modelDeployments) join only when enableFullPanel = true.
// Same source feeds the openai module above (ARM deployment) and AZURE_MODELS below (runtime config).
var activeModels = enableFullPanel ? concat([screenerModel], modelDeployments) : [screenerModel]

var runtimeModels = [for m in activeModels: {
  name: m.name
  deployment: m.deployment
  protocol: m.protocol
  baseURL: m.baseURL
  authScope: m.authScope
  tpm: m.tpm
  rpm: m.rpm
  wantLogprobs: m.wantLogprobs
}]

var azureModelsJson = string(runtimeModels)

var azureScreeningModel = screenerModel.name

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
    contributorId: contributorId
    environmentId: containerAppsEnv.outputs.id
    uamiId: jobIdentity.outputs.id
    uamiClientId: jobIdentity.outputs.clientId
    acrLoginServer: registry.outputs.loginServer
    containerImage:  empty(containerImage) ? '${registry.outputs.loginServer}/jobfinder:latest' : containerImage
    openAiEndpoint: openai.outputs.endpoint
    storageAccountName: storage.outputs.name
    azureModelsJson: azureModelsJson
    azureScreeningModel: azureScreeningModel
    postgresFqdn: postgres.outputs.fqdn
    postgresDatabaseName: postgres.outputs.databaseName
    postgresAppPrincipalName: jobIdentity.outputs.name
    keyVaultUri: keyVault.outputs.uri
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
    jobName: '${namePrefix}-job'
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
