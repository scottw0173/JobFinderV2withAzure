using 'main.bicep'

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

// Leave empty to use the Go code's defaultAzureModels (the CLAUDE.md §12
// 12-model panel).
param azureModelsJson = ''

// Leave empty to default to '<acrLoginServer>/jobfinder:latest' - push the
// image manually (no CI/CD wired yet) before running the Job.
param containerImage = ''
