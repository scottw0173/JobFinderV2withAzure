package main

import (
	"context"
	"os"
	"testing"
)

func TestResolveProviderKeyEnvOverride(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "fake-key-value")
	t.Setenv("KEYVAULT_URI", "") // must not be required for the override path

	got, err := resolveProviderKey(context.Background(), "GEMINI-API-KEY")
	if err != nil {
		t.Fatalf("resolveProviderKey: %v", err)
	}
	if got != "fake-key-value" {
		t.Errorf("resolveProviderKey() = %q, want %q", got, "fake-key-value")
	}
}

func TestResolveProviderKeyNoOverrideNoVaultURI(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("KEYVAULT_URI", "")

	if _, err := resolveProviderKey(context.Background(), "GEMINI-API-KEY"); err == nil {
		t.Fatal("expected an error when neither the env override nor KEYVAULT_URI is set")
	}
}

// TestResolveProviderKeyLive exercises the real Key Vault path end to end:
// newAzureCredential -> azsecrets client -> GetSecret against a live vault.
//
// It is a MANUAL smoke test, not part of the CI gate. It self-skips unless
// KEYVAULT_URI is set, exactly like postgres_aad_test.go's
// TestKeylessPostgresSmoke, so `go test ./...` stays green with nothing
// provisioned.
//
// Run from an environment where DefaultAzureCredential resolves to a
// principal holding Key Vault Secrets User on the vault at KEYVAULT_URI:
//   - locally: `az login` as a principal granted that role;
//   - in prod: the app UAMI on the Container Apps Job.
//
// Only the resolved key's length is logged - never the value - per the
// brief's "confirms a non-empty value comes back (do not log the value)".
func TestResolveProviderKeyLive(t *testing.T) {
	vaultURI := os.Getenv("KEYVAULT_URI")
	if vaultURI == "" {
		t.Skip("KEYVAULT_URI not set; skipping live key vault smoke test")
	}
	// Force the real Key Vault path even if a stray env override happens to
	// be set in this shell - the point of this test is proving the identity
	// chain, not the override short-circuit (covered above).
	t.Setenv("GEMINI_API_KEY", "")

	got, err := resolveProviderKey(context.Background(), "GEMINI-API-KEY")
	if err != nil {
		t.Fatalf("resolveProviderKey against live vault %s failed: %v", vaultURI, err)
	}
	if got == "" {
		t.Fatal("resolveProviderKey returned an empty value with no error")
	}
	t.Logf("OK: resolved GEMINI-API-KEY from %s (%d chars)", vaultURI, len(got))
}
