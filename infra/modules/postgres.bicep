@description('Name of the PostgreSQL Flexible Server.')
param name string

@description('Azure region for the server.')
param location string

@description('SKU name, e.g. Standard_B1ms for the smallest viable Burstable tier.')
param skuName string = 'Standard_B1ms'

@description('SKU tier.')
param skuTier string = 'Burstable'

@description('PostgreSQL major version.')
param postgresVersion string = '16'

@description('Name of the application database.')
param databaseName string = 'jobfinder'

@description('Entra object ID of the human/service principal to register as Postgres AAD administrator.')
param adminObjectId string

@description('Principal name (UPN or display name) matching adminObjectId.')
param adminPrincipalName string

@description('Principal type for the human/service admin - "User" or "ServicePrincipal".')
param adminPrincipalType string = 'User'

@description('Object ID of the deploymentScript\'s own user-assigned identity - must ALSO be registered as a Postgres AAD administrator so it can execute pgaadauth_create_principal (a non-admin principal cannot grant itself that role).')
param scriptIdentityObjectId string

@description('Principal name matching scriptIdentityObjectId.')
param scriptIdentityPrincipalName string

@description('Resource ID of the user-assigned identity the deploymentScript executes as (same identity as scriptIdentityObjectId).')
param scriptIdentityResourceId string

@description('Name of the target application-side identity (the Container Apps Job\'s UAMI) to create as a non-admin Postgres database principal.')
param appIdentityName string

// authConfig.passwordAuth is 'Disabled' - no password exists anywhere for
// this server, not even as a @secure() param. administratorLogin/
// administratorLoginPassword are deliberately omitted: AAD-only auth is
// configured entirely via the `administrators` sub-resource below, not via
// a traditional login. NOTE: if bicep build or a real deployment reveals
// these properties are still mandatory on this API version, they'll need
// to be added back - flagged as unverified rather than guessed.
resource server 'Microsoft.DBforPostgreSQL/flexibleServers@2024-08-01' = {
  name: name
  location: location
  sku: {
    name: skuName
    tier: skuTier
  }
  properties: {
    version: postgresVersion
    authConfig: {
      activeDirectoryAuth: 'Enabled'
      passwordAuth: 'Disabled'
    }
    network: {
      publicNetworkAccess: 'Enabled'
    }
    // Proportionate to this project's scale - a single-purpose batch job,
    // not a public-facing service. VNet/private-endpoint noted as a
    // documented hardening option for later, not built now (mirrors the
    // alpine-vs-distroless "harden later" precedent from step 5).
    storage: {
      storageSizeGB: 32
    }
  }
}

resource allowAzureServices 'Microsoft.DBforPostgreSQL/flexibleServers/firewallRules@2024-08-01' = {
  parent: server
  name: 'AllowAzureServices'
  properties: {
    startIpAddress: '0.0.0.0'
    endIpAddress: '0.0.0.0'
  }
}

resource database 'Microsoft.DBforPostgreSQL/flexibleServers/databases@2024-08-01' = {
  parent: server
  name: databaseName
}

// Human/service AAD administrator - full admin rights, matches the
// deploying operator.
resource humanAdmin 'Microsoft.DBforPostgreSQL/flexibleServers/administrators@2024-08-01' = {
  parent: server
  name: adminObjectId
  properties: {
    principalName: adminPrincipalName
    principalType: adminPrincipalType
    tenantId: subscription().tenantId
  }
}

// The deploymentScript's own identity must ALSO be an admin, since only an
// existing admin connection can call pgaadauth_create_principal to grant a
// non-admin principal to the Job's UAMI below. This is the "explicit
// database-principal creation (not plain RBAC)" mechanism CLAUDE.md's hard
// rule calls for.
resource scriptAdmin 'Microsoft.DBforPostgreSQL/flexibleServers/administrators@2024-08-01' = {
  dependsOn: [ humanAdmin ]
  parent: server
  name: scriptIdentityObjectId
  properties: {
    principalName: scriptIdentityPrincipalName
    principalType: 'ServicePrincipal'
    tenantId: subscription().tenantId
  }
}

// HIGHEST-UNCERTAINTY RESOURCE IN THE WHOLE TEMPLATE. bicep build validates
// this resource's own ARM shape (script kind, version, timeout, storage
// account settings) but CANNOT validate that the embedded az/psql
// invocation is correct, that pgaadauth_create_principal behaves as
// documented against the chosen Postgres version, or that the two-admin-
// identity ordering (scriptAdmin must exist before this runs) actually
// works at deploy time - all of that needs a live subscription this
// project doesn't have yet.
resource createAppPrincipal 'Microsoft.Resources/deploymentScripts@2023-08-01' = {
  name: 'create-${appIdentityName}-pg-principal'
  location: location
  kind: 'AzureCLI'
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${scriptIdentityResourceId}': {}
    }
  }
  properties: {
    azCliVersion: '2.65.0'
    retentionInterval: 'P1D'
    timeout: 'PT15M'
    cleanupPreference: 'OnSuccess'
    scriptContent: '''
      set -e
      az login --identity  --allow-no-subscriptions
      tdnf install -y postgresql
      export PGPASSWORD=$(az account get-access-token --resource-type oss-rdbms --query accessToken -o tsv)
      psql "host=${SERVER_FQDN} port=5432 dbname=postgres user=${SCRIPT_IDENTITY_NAME} sslmode=require" \
        -c "SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '${APP_IDENTITY_NAME}') THEN pgaadauth_create_principal('${APP_IDENTITY_NAME}', false, false) END;"
    '''
    environmentVariables: [
      {
        name:'SERVER_FQDN'
        value: server.properties.fullyQualifiedDomainName
      }
      /*
      {
        name: 'SERVER_NAME'
        value: server.name
      }
        */
      {
        name: 'SCRIPT_IDENTITY_NAME'
        value: scriptIdentityPrincipalName
      }
      {
        name: 'APP_IDENTITY_NAME'
        value: appIdentityName
      }
    ]
  }
  dependsOn: [
    scriptAdmin
  ]
}

output fqdn string = server.properties.fullyQualifiedDomainName
output name string = server.name
output databaseName string = database.name
