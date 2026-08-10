@description('Name of the Cognitive Services / AI Foundry account.')
param name string

@description('Azure region for the account. Must carry the target model catalog - verify against the live Azure AI Foundry model catalog before deployment.')
param location string

@description('SKU for the account.')
param skuName string = 'S0'

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

resource gpt54mini 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.4-mini'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.4-mini'           // wire model name
      version: '2026-03-17'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

resource gpt53codex 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.3-codex'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.3-codex'           // wire model name
      version: '2026-02-24'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

resource gpt54nano 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.4-nano'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 2500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.4-nano'           // wire model name
      version: '2026-03-17'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

resource gpt54regular 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.4'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.4'           // wire model name
      version: '2026-03-05'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

resource gpt55regular 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.5'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.5'           // wire model name
      version: '2026-04-24'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

resource gpt56sol 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.6-sol'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.6-sol'           // wire model name
      version: '2026-07-09'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

resource gpt56luna 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.6-luna'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.6-luna'           // wire model name
      version: '2026-07-09'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

resource gpt56terra 'Microsoft.CognitiveServices/accounts/deployments@2025-06-01' = {
  parent: account
  name: 'gpt-5.6-terra'          //chosen deployment name — this is what the scorer targets
  sku: {
    name: 'GlobalStandard'     
    capacity: 500                // TPM in thousands
  }
  properties: {
    model: {
      format: 'OpenAI'          
      name: 'gpt-5.6-terra'           // wire model name
      version: '2026-07-09'     
    }
    versionUpgradeOption: 'NoAutoUpgrade'
  }
}

output id string = account.id
output endpoint string = account.properties.endpoint
output name string = account.name

