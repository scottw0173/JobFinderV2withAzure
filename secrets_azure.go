package main

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// azureSecrets is a placeholder until the Azure subscription is live and
// keyless managed-identity/Entra auth is wired up (pairs with the Bicep work
// in migration step 7). No connection strings or keys belong in this file.
// The keyless-auth mechanism itself (below) is a separate concern from
// named-secret lookup - Fetch stays a stub, untouched by it.
type azureSecrets struct{}

func newAzureSecrets() *azureSecrets { return &azureSecrets{} }

func (s *azureSecrets) Fetch(ctx context.Context, name string) (string, error) {
	return "", traceErrorf("azure secrets require managed identity - not available before the Azure subscription is live")
}

// newAzureCredential constructs the managed-identity credential used for
// both Postgres (via newBeforeConnectHook) and Azure OpenAI (via
// openaiScorer.authHeaderValue) - keyless-auth structure.
// AZURE_CLIENT_ID selects the user-assigned identity containerAppsJob.bicep
// wires onto the Job (not a system-assigned identity); falls back to
// default options if unset. Construction never touches the network, so
// this is safe to call unconditionally, including in local dev where the
// resulting credential is simply never exercised.
func newAzureCredential() (azcore.TokenCredential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, wrapErr("constructing managed identity credential", err)
	}
	return cred, nil
}

// postgresAADScope is the Entra scope for Postgres Flexible Server's
// AAD-auth data plane. UNVERIFIED - noted from memory; do
// not trust as known-correct until confirmed against a live Azure account.
const postgresAADScope = "https://ossrdbms-aad.database.windows.net/.default"

// newBeforeConnectHook returns a pgxpool BeforeConnect func that fetches a
// fresh Entra token and uses it as the connection password - Postgres has
// passwordAuth: 'Disabled' (postgres.bicep), so AAD is the only auth path
// for the real deployment. No custom caching/refresh logic: BeforeConnect
// already fires on every new physical connection, and azidentity
// credentials cache/refresh internally, so fetching fresh each time is
// sufficient.
func newBeforeConnectHook(cred azcore.TokenCredential, scope string) func(ctx context.Context, cfg *pgx.ConnConfig) error {
	return func(ctx context.Context, cfg *pgx.ConnConfig) error {
		token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
		if err != nil {
			return wrapErr("fetching postgres AAD token", err)
		}
		cfg.Password = token.Token
		return nil
	}
}

// dsnHasPassword reports whether a parsed pgxpool config already carries a
// password - local dev's docker-compose DSN does; the Bicep-deployed
// AAD-only DSN (postgres.bicep: passwordAuth 'Disabled') deliberately
// doesn't. wireAzure() uses this to decide whether to attach
// newBeforeConnectHook at all, so local dev's real static password is never
// overridden by a token.
func dsnHasPassword(cfg *pgxpool.Config) bool {
	return cfg.ConnConfig.Password != ""
}
