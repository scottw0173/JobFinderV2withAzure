using 'main.bicep'

param extraOpenAiAccountIds = []

param namePrefix = 'jf-dev'
param location = 'westus3'

// Supply the real values for your tenant before deploying - these have no
// safe defaults (Postgres AAD admin registration needs a real principal).
param postgresAdminObjectId = '00000000-0000-0000-0000-000000000000'
param postgresAdminPrincipalName = 'admin@example.com'

param acrSku = 'Basic'
param postgresSkuName = 'Standard_B1ms'
param postgresSkuTier = 'Burstable'
param openAiSkuName = 'S0'

param enableFullPanel = false

// Tier-0 screener - single source of truth (main.bicep) for both the openai
// module's ARM deployment and the runtime AZURE_MODELS JSON. version/capacity
// verified against the live catalog/account (capacity is TPM in thousands).
param screenerModel = {
  name: 'gpt-5-mini'
  model: 'gpt-5-mini'
  version: '2025-08-07'
  capacity: 500
  deployment: 'gpt-5-mini'
  protocol: 'openai'
  authScope: 'https://ai.azure.com/.default'
  tpm: 500000
  rpm: 500
  wantLogprobs: false
}

// Leave empty to default to '<acrLoginServer>/jobfinder:latest' - push the
// image manually (no CI/CD wired yet) before running the Job.
param containerImage = ''
