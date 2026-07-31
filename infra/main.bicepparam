using 'main.bicep'

param extraOpenAiAccountIds = [
  '/subscriptions/5a1080c6-40d1-4367-a37d-ea6aff4c4824/resourceGroups/jobfinder-rg/providers/Microsoft.CognitiveServices/accounts/jobfinderv2-resource'
]

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
param azureModelsJson = '''
[
{"name":"Kimi-K2.5","deployment":"Kimi-K2.5","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":20000,"rpm":20,"wantLogprobs":true},
{"name":"gpt-5.4-mini","deployment":"gpt-5.4-mini","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":200000,"rpm":1000,"wantLogprobs":true},
{"name":"gpt-5.3-codex","deployment":"gpt-5.3-codex","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":500000,"rpm":5000,"wantLogprobs":true},
{"name":"gpt-5.4-nano","deployment":"gpt-5.4-nano","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":2500000,"rpm":2500,"wantLogprobs":true},
{"name":"gpt-5.4","deployment":"gpt-5.4","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":500000,"rpm":5000,"wantLogprobs":true},
{"name":"gpt-5.5","deployment":"gpt-5.5","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":500000,"rpm":500,"wantLogprobs":true},
{"name":"gpt-5.6-terra","deployment":"gpt-5.6-terra","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":500000,"rpm":500,"wantLogprobs":true},
{"name":"gpt-5.6-luna","deployment":"gpt-5.6-luna","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":500000,"rpm":500,"wantLogprobs":true},
{"name":"gpt-5.6-sol","deployment":"gpt-5.6-sol","protocol":"openai","baseURL":"https://jobfinderv2-resource.services.ai.azure.com/openai/v1","authScope":"https://ai.azure.com/.default","tpm":500000,"rpm":500,"wantLogprobs":true}
]
'''

//This model, by name, is what the program will pull for filling out the panel_jobs table
param azureScreeningModel = 'gpt-5.4-mini'

// Leave empty to default to '<acrLoginServer>/jobfinder:latest' - push the
// image manually (no CI/CD wired yet) before running the Job.
param containerImage = ''
