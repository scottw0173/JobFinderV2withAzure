package main

import (
	"context"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

// resolveProviderKey resolves a provider API key by Key Vault secret name
// (e.g. "GEMINI-API-KEY"). This is deliberately separate from any Scorer: a
// scorer receives its key as a constructor parameter and must never reach
// into Key Vault, env vars, or any secret store itself (keeps the scorer
// provider-generic and testable with a canned key, per CLAUDE.md §9).
//
// Resolution order:
//  1. Env override — secretName with hyphens replaced by underscores (e.g.
//     GEMINI-API-KEY -> GEMINI_API_KEY). Local-dev/compose path: no Azure
//     identity required.
//  2. Key Vault, at the URI in KEYVAULT_URI (injected by infra), via
//     DefaultAzureCredential (the app UAMI in a deployed run; falls back to
//     CLI/env creds locally). The credential is handed to the azsecrets
//     client directly rather than used to hand-mint a token: the Key Vault
//     audience differs from the Postgres AAD scope, and the SDK's challenge
//     policy negotiates it.
func resolveProviderKey(ctx context.Context, secretName string) (string, error) {
	envVar := strings.ReplaceAll(secretName, "-", "_")
	if override := os.Getenv(envVar); override != "" {
		return override, nil
	}

	vaultURI := os.Getenv("KEYVAULT_URI")
	if vaultURI == "" {
		return "", traceErrorf("KEYVAULT_URI not set and no %s override present for secret %q", envVar, secretName)
	}

	cred, err := newAzureCredential()
	if err != nil {
		return "", wrapErr("constructing credential to resolve secret "+secretName, err)
	}

	client, err := azsecrets.NewClient(vaultURI, cred, nil)
	if err != nil {
		return "", wrapErr("constructing key vault client for "+vaultURI, err)
	}

	resp, err := client.GetSecret(ctx, secretName, "", nil)
	if err != nil {
		return "", wrapErr("fetching secret "+secretName, err)
	}
	if resp.Value == nil || *resp.Value == "" {
		return "", traceErrorf("secret %q has no value in key vault %s", secretName, vaultURI)
	}
	return *resp.Value, nil
}
