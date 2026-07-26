package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestKeylessPostgresSmoke exercises the real managed-identity Postgres path
// end to end: newAzureCredential -> token for postgresAADScope -> the actual
// newBeforeConnectHook -> Ping -> trivial query, against a live Flexible Server.
//
// It is a MANUAL smoke test, not part of the CI gate. It self-skips unless
// POSTGRES_AAD_DSN is set, exactly like store_azure_test.go's POSTGRES_TEST_DSN
// guard, so `go test ./...` in CI stays green with nothing provisioned.
//
// POSTGRES_AAD_DSN must be the AAD-only (passwordless) DSN shape, e.g.:
//
//	postgres://<aad-principal>@<server>.postgres.database.azure.com:5432/jobfinder?sslmode=require
//
// Run from an environment where DefaultAzureCredential resolves to a principal
// that has been granted a Postgres role:
//   - locally: `az login` as the Entra admin/user added to the Flexible Server
//     (your client IP must be allowed through the server firewall);
//   - in prod: the user-assigned managed identity on the Container Apps Job.
//
// A local pass proves the scope, DSN username shape, SSL, and DB-side role
// mapping are correct. The final leg — that the *specific* managed identity
// works — is only proven in the first deployed run, since you can't fully
// impersonate that identity locally.
func TestKeylessPostgresSmoke(t *testing.T) {
	dsn := os.Getenv("POSTGRES_AAD_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_AAD_DSN not set; skipping keyless Postgres smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cred, err := newAzureCredential()
	if err != nil {
		t.Fatalf("newAzureCredential: %v", err)
	}

	// Step 1 — prove the scope/identity BEFORE touching Postgres, so a scope
	// failure is unambiguously distinct from a DB/role failure downstream.
	tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{postgresAADScope}})
	if err != nil {
		t.Fatalf("GetToken for %s failed — scope wrong or identity can't mint this token: %v", postgresAADScope, err)
	}
	t.Logf("step 1 OK: token for %s (expires %s)", postgresAADScope, tok.ExpiresOn.UTC().Format(time.RFC3339))

	// Step 2 — assemble the pool exactly like wireAzure: parse, confirm the
	// passwordless AAD shape, attach the REAL BeforeConnect hook.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if dsnHasPassword(cfg) {
		t.Fatal("POSTGRES_AAD_DSN carries a password; use the AAD-only shape so the BeforeConnect hook is actually exercised")
	}
	cfg.BeforeConnect = newBeforeConnectHook(cred, postgresAADScope)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	defer pool.Close()

	// Step 3 — Ping proves the token was accepted as a Postgres role. The most
	// common failure here is "token fine, DB rejects it": role not provisioned,
	// wrong DSN username (must be the AAD principal name), or firewall.
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("step 3 FAIL: token acquired but DB rejected the connection "+
			"(role not provisioned / wrong DSN username / firewall): %v", err)
	}

	// Step 4 — confirm which role we authenticated as, and that we can query.
	var user, version string
	if err := pool.QueryRow(ctx, "SELECT current_user, version()").Scan(&user, &version); err != nil {
		t.Fatalf("SELECT current_user/version failed: %v", err)
	}
	t.Logf("step 4 OK: connected as %q", user)
	t.Logf("server: %s", version)

	// Step 5 — readiness bonus: is the migration applied? Lets this smoke test
	// double as the pre-first-run check for the panel table.
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'panel_jobs')`,
	).Scan(&exists); err != nil {
		t.Fatalf("checking panel_jobs existence: %v", err)
	}
	if exists {
		t.Log("step 5 OK: panel_jobs present — schema migration confirmed")
	} else {
		t.Log("step 5 WARNING: connected fine, but panel_jobs does not exist — apply the schema migration before the first panel build")
	}
}
