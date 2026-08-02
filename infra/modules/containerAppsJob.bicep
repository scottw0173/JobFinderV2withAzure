@description('Name of the Container Apps Job.')
param name string

@description('Azure region for the Job.')
param location string

@description('Resource ID of the Container Apps managed Environment.')
param environmentId string

@description('Resource ID of the Job\'s user-assigned identity.')
param uamiId string

@description('Client ID of the Job\'s UAMI, injected as AZURE_CLIENT_ID so the Go managed-identity credential picks the right identity.')
param uamiClientId string

@description('ACR login server, e.g. myregistry.azurecr.io.')
param acrLoginServer string

@description('Full container image reference to run, e.g. myregistry.azurecr.io/jobfinder:latest.')
param containerImage string

@description('Azure OpenAI/Foundry account endpoint.')
param openAiEndpoint string

@description('Name of the storage account holding the config blob container - injected as AZURE_STORAGE_ACCOUNT so the blob-backed ConfigSource (config_azure.go) knows which account to read from.')
param storageAccountName string

@description('JSON-encoded model list override matching ModelConfig - omit to use the Go code\'s defaultAzureModels.')
param azureModelsJson string = ''

@description('Name of LLM model to use as screener. omit to use the default- DeepSeekV4-Flash')
param azureScreeningModel string = ''

@description('Path inside the container where config files are expected - dev-loop fallback only. config_azure.go reads this path when AZURE_STORAGE_ACCOUNT is unset; when set, it downloads from the storage account\'s config blob container via managed identity instead.')
param azureConfigDir string = '/config'

@description('Postgres server FQDN.')
param postgresFqdn string

@description('Postgres database name.')
param postgresDatabaseName string

@description('Name of the Postgres database principal created for this Job\'s identity (see postgres.bicep\'s deploymentScript).')
param postgresAppPrincipalName string

@description('Cron schedule for the daily run - matches AWS template.yaml\'s cron(0 13 * * ? *) UTC time.')
param cronSchedule string = '0 13 * * *'

var baseEnv = [
  {
  // ID for user, specifically for data-evaluation purposes
  // set to "test" currently for trial cron run
  name: 'AZURE_CONTRIBUTOR_ID'
  value: 'test'
  }
  // resume_id and config_id are no longer env-set: both are content hashes
  // computed at wire time from instructions.md / sources.json /
  // filterKeywords.json / panel knobs (CLAUDE.md's config_id definition) -
  // an env var here could silently diverge from the hash it's supposed to
  // represent, which is exactly what moving to a content hash avoids.
  {
  // Needed to avoid 400 error during fetch of AAD token
  // without this, you will  get ManagedIdentityCredential error  
  name: 'AZURE_CLIENT_ID'
  value: uamiClientId
  }
  {
    // Azure-exclusive infra by design - CLAUDE.md: "Only the
    // deployment/infra is Azure-exclusive on this branch." Not
    // parametrized; this Job only ever wires the Azure path.
    name: 'CLOUD_PROVIDER'
    value: 'azure'
  }
  {
    name: 'AZURE_CONFIG_DIR'
    value: azureConfigDir
  }
  {
    name: 'AZURE_OPENAI_ENDPOINT'
    value: openAiEndpoint
  }
  {
    name: 'AZURE_STORAGE_ACCOUNT'
    value: storageAccountName
  }
  {
    // No password here because postgres.bicep disables password auth
    // entirely (passwordAuth: 'Disabled') - the Go side fills it in at
    // connect time with a fresh Entra token (newBeforeConnectHook in
    // secrets_azure.go, wired in main.go's wireAzure). Confirmed working
    // against a live Job run: pool.Ping succeeds via this AAD-token-as-
    // password path.
    name: 'POSTGRES_DSN'
    value: 'postgres://${postgresAppPrincipalName}@${postgresFqdn}:5432/${postgresDatabaseName}?sslmode=require'
  }
]

var screenerEnv = empty(azureScreeningModel) ? [] : [
 {
    name: 'AZURE_SCREENING_MODEL'
    value: azureScreeningModel
  }
]

var modelsEnv = empty(azureModelsJson) ? [] : [
  {
    name: 'AZURE_MODELS'
    value: azureModelsJson
  }
]

resource job 'Microsoft.App/jobs@2024-03-01' = {
  name: name
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${uamiId}': {}
    }
  }
  properties: {
    environmentId: environmentId
    configuration: {
      triggerType: 'Schedule'
      scheduleTriggerConfig: {
        cronExpression: cronSchedule
        parallelism: 1
        replicaCompletionCount: 1
      }
      replicaTimeout: 18000
      replicaRetryLimit: 0
      registries: [
        {
          server: acrLoginServer
          identity: uamiId
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'jobfinder'
          image: containerImage
          resources: {
            cpu: json('1.0')
            memory: '2Gi'
          }
          env: concat(baseEnv, modelsEnv, screenerEnv)
        }
      ]
    }
  }
}

output name string = job.name
output id string = job.id
