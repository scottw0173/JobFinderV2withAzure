@description('Name of the Cognitive Services / AI Foundry account.')
param name string

@description('Azure region for the account. Must carry the target model catalog - verify against the live Azure AI Foundry model catalog before deployment.')
param location string

@description('SKU for the account.')
param skuName string = 'S0'

@description('Two-tier deploy gate: false deploys only screenerModel; true adds modelDeployments (the tier-1 graders) alongside it.')
param enableFullPanel bool = false

@description('Tier-0 screening model ({name, model, version, capacity}). Always deployed regardless of enableFullPanel — required, no default here; main.bicep is the single source of truth (see main.bicepparam for the real value).')
param screenerModel object = { name: 'gpt-5-mini', model: 'gpt-5-mini', version: '2025-08-07', capacity: 150}

@description('First-party Foundry model deployments. Capacities are subscription/region-specific — override per fork.')
param modelDeployments array = [
  { name: 'gpt-5.4-mini',  model: 'gpt-5.4-mini',  version: '2026-03-17', capacity: 500 }
  { name: 'gpt-5.3-codex', model: 'gpt-5.3-codex', version: '2026-02-24', capacity: 500 }
  { name: 'gpt-5.4-nano',  model: 'gpt-5.4-nano',  version: '2026-03-17', capacity: 2500 }
  { name: 'gpt-5.4',       model: 'gpt-5.4',       version: '2026-03-05', capacity: 500 }
  { name: 'gpt-5.5',       model: 'gpt-5.5',       version: '2026-04-24', capacity: 500 }
  { name: 'gpt-5.6-sol',   model: 'gpt-5.6-sol',   version: '2026-07-09', capacity: 500 }
  { name: 'gpt-5.6-luna',  model: 'gpt-5.6-luna',  version: '2026-07-09', capacity: 500 }
  { name: 'gpt-5.6-terra', model: 'gpt-5.6-terra', version: '2026-07-09', capacity: 500 }
]

resource account 'Microsoft.CognitiveServices/accounts@2025-06-01' = {
  name: name
  location: location
  kind: 'AIServices'
  sku: {
    name: skuName
  }
  properties: {
    publicNetworkAccess: 'Enabled'
    // Required for Entra/AAD-based data-plane auth - without this, only
    // key-based auth is available.
    customSubDomainName: name
    // Forces Entra-only auth, no API-key path - the keyless enforcement
    // knob for this resource, per the "no keys in code or Bicep" rule.
    disableLocalAuth: true
  }
}

// Screener always deploys; graders (modelDeployments) join only when enableFullPanel = true.
var activeModels = enableFullPanel ? concat([screenerModel], modelDeployments) : [screenerModel]

@batchSize(1)
resource deployments 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = [
  for m in activeModels: {
    parent: account
    name: m.name
    sku: { name: 'GlobalStandard', capacity: m.capacity }
    properties: {
      model: { format: 'OpenAI', name: m.model, version: m.version }
      versionUpgradeOption: 'NoAutoUpgrade'
    }
  }
]

output id string = account.id
output endpoint string = account.properties.endpoint
output name string = account.name
