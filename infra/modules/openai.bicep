@description('Name of the Cognitive Services / AI Foundry account.')
param name string

@description('Azure region for the account. Must carry the target model catalog - verify against the live Azure AI Foundry model catalog before deployment.')
param location string

@description('SKU for the account.')
param skuName string = 'S0'

// Modern unified "Foundry resource" kind, spanning both OpenAI-native
// models and the broader Foundry model catalog.
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

// gpt-4.1-mini: OpenAI-native, deploys via the standard OpenAI model
// format. High confidence in this shape.
/*resource gpt41Mini 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-4.1-mini'
  sku: {
    name: 'GlobalStandard'
    capacity: 10
  }
  properties: {
    model: {
      format: 'OpenAI'
      name: 'gpt-4.1-mini'
      version: '2025-04-14'
    }
  }
}

// phi-4: Foundry "Models-as-a-Service" catalog entry, not OpenAI-native.
// Authored using the same accounts/deployments shape for template
// consistency, but the exact model.version/SKU/regional availability has
// NOT been verified against the live Foundry catalog - bicep build only
// proves this resource shape compiles, not that this model is purchasable
// in the chosen region. Verify before actual deployment.
resource phi4 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'phi-4'
  sku: {
    name: 'GlobalStandard'
    capacity: 10
  }
  properties: {
    model: {
      format: 'Microsoft'
      name: 'Phi-4'
      version: '1'
    }
  }
}

// llama-3.3-70b: Foundry Models-as-a-Service catalog entry (Meta). Same
// verification caveat as phi-4 above.
resource llama33 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'llama-3.3-70b'
  sku: {
    name: 'GlobalStandard'
    capacity: 10
  }
  properties: {
    model: {
      format: 'Meta'
      name: 'Llama-3.3-70B-Instruct'
      version: '1'
    }
  }
}

// deepseek-v3: Foundry Models-as-a-Service catalog entry (DeepSeek). Same
// verification caveat as phi-4 above.
resource deepseekV3 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'deepseek-v3'
  sku: {
    name: 'GlobalStandard'
    capacity: 10
  }
  properties: {
    model: {
      format: 'DeepSeek'
      name: 'DeepSeek-V3'
      version: '1'
    }
  }
}
*/
output id string = account.id
output endpoint string = account.properties.endpoint
output name string = account.name

