package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeTokenCredential is the cheapest possible test double for
// azcore.TokenCredential (CLAUDE.md §9 Tier 2) - a canned token or a canned
// error, nothing resembling a realistic Azure SDK mock. Shared by
// secrets_azure_test.go and scorer_openai_test.go.
type fakeTokenCredential struct {
	token string
	err   error
}

func (f *fakeTokenCredential) GetToken(ctx context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if f.err != nil {
		return azcore.AccessToken{}, f.err
	}
	return azcore.AccessToken{Token: f.token}, nil
}

var errTestCredential = errors.New("fake credential error")

func TestNewBeforeConnectHookSetsPasswordFromToken(t *testing.T) {
	cred := &fakeTokenCredential{token: "fake-postgres-token"}
	hook := newBeforeConnectHook(cred, postgresAADScope)

	cfg := &pgx.ConnConfig{}
	if err := hook(context.Background(), cfg); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if cfg.Password != "fake-postgres-token" {
		t.Errorf("cfg.Password = %q, want %q", cfg.Password, "fake-postgres-token")
	}
}

func TestNewBeforeConnectHookPropagatesCredentialError(t *testing.T) {
	cred := &fakeTokenCredential{err: errTestCredential}
	hook := newBeforeConnectHook(cred, postgresAADScope)

	if err := hook(context.Background(), &pgx.ConnConfig{}); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestDsnHasPassword(t *testing.T) {
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGPASSFILE", "/dev/null")

	tests := []struct {
		name string
		dsn  string
		want bool
	}{
		{
			name: "local dev DSN carries a password",
			dsn:  "postgres://jobfinder:jobfinder_local_dev@localhost:5432/jobfinder?sslmode=disable",
			want: true,
		},
		{
			name: "AAD-only DSN has no password (postgres.bicep's passwordAuth: Disabled shape)",
			dsn:  "postgres://someuami@myserver.postgres.database.azure.com:5432/jobfinder?sslmode=require",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig(tt.dsn)
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if got := dsnHasPassword(cfg); got != tt.want {
				t.Errorf("dsnHasPassword() = %v, want %v", got, tt.want)
			}
		})
	}
}
