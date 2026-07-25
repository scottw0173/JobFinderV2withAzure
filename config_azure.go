package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

// azureConfigContainer is the blob container config files live in - matches
// storage.bicep's containerName default. Not parametrized: CLAUDE.md's
// blob-backed-ConfigSource decision (§2 task 1) branches only on
// AZURE_STORAGE_ACCOUNT; a second env var for the container name would be
// unexercised config surface for a value that isn't expected to vary.
const azureConfigContainer = "config"

// azureConfigSource is functional now, unlike the other Azure stubs: handler()
// returns fatally if ConfigSource.File errors (via LoadKeywordFilter), so an
// azure run would die at startup without a working implementation. It reads
// from the `config` blob container via managed identity when
// AZURE_STORAGE_ACCOUNT is set (the deployed Job); otherwise it falls back to
// a local directory (AZURE_CONFIG_DIR), which is what the docker-compose dev
// loop uses and must keep working unchanged.
type azureConfigSource struct {
	dir  string
	blob *azblob.Client // nil when falling back to the local directory
}

func newAzureConfigSource(cred azcore.TokenCredential) (*azureConfigSource, error) {
	dir := os.Getenv("AZURE_CONFIG_DIR")
	if dir == "" {
		dir = "./config"
	}
	src := &azureConfigSource{dir: dir}
	if account := os.Getenv("AZURE_STORAGE_ACCOUNT"); account != "" {
		client, err := azblob.NewClient("https://"+account+".blob.core.windows.net/", cred, nil)
		if err != nil {
			return nil, wrapErr("constructing azure blob client", err)
		}
		src.blob = client
	}
	return src, nil
}

func (c *azureConfigSource) File(ctx context.Context, name string) ([]byte, error) {
	if c.blob == nil {
		data, err := os.ReadFile(filepath.Join(c.dir, name))
		if err != nil {
			return nil, wrapErr("read azure config file "+name, err)
		}
		return data, nil
	}
	resp, err := c.blob.DownloadStream(ctx, azureConfigContainer, name, nil)
	if err != nil {
		return nil, wrapErr("download azure config blob "+name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, wrapErr("read azure config blob "+name, err)
	}
	return data, nil
}

// defaultAzureModels is the 12-model panel CLAUDE.md §12 selected (open-weight
// + current-generation + chat-capable), in the table's row order. Every
// launch-day VERIFY field - BaseURL, Deployment, TPM, RPM, AuthScope - is
// deliberately left at its zero value: those are exactly §12's "Serving
// (VERIFY)" column (native Foundry vs. Fireworks-served deployments sit at
// different endpoints), and §9 says never fabricate an account-dependent
// value. Consequence: handler()'s TPM/RPM==0 and BaseURL=="" gates will
// refuse to score every model here until the real values land - intended,
// not a bug, until the Azure account exists. This is the opposite intent
// from the old 4-entry stub this replaced, which pointed at local Ollama
// because those names were meant to be literally Ollama-testable; these 12
// are real frontier open-weight models (26B-397B) that can't run on this
// machine at all (see the Ollama-hardware-limits memory), so a fake
// localhost BaseURL would be actively misleading rather than a harmless
// placeholder. Protocol is the one field set now: "openai" is §12's
// documented build-now prior (near-certainly OpenAI-compatible for all 12),
// which isn't account-dependent. Local Ollama smoke testing continues to go
// through the AZURE_MODELS env override (docker-compose.yml), untouched by
// this list.
var defaultAzureModels = []ModelConfig{
	{Name: "DeepSeek-V4-Pro", Protocol: "openai"},   // DeepSeek, MoE, native or Fireworks
	{Name: "DeepSeek-V4-Flash", Protocol: "openai"}, // DeepSeek, MoE, native or Fireworks
	{Name: "Kimi-K2.6", Protocol: "openai"},         // Moonshot, MoE, native or Fireworks
	{
		Name:         "Kimi-K2.5",
		Deployment:   "Kimi-K2.5",
		Protocol:     "openai",
		BaseURL:      "https://jobfinderv2-resource.services.ai.azure.com/openai/v1",
		AuthScope:    "https://ai.azure.com/.default",
		TPM:          20000,
		RPM:          20,
		WantLogprobs: true,
	},
	{Name: "MiniMax-M2.5", Protocol: "openai"},               // MiniMax, MoE, Fireworks (FW-)
	{Name: "GLM-5.2", Protocol: "openai"},                    // Zhipu, MoE, Fireworks (FW-)
	{Name: "Nemotron-3-Super-120B-A12B", Protocol: "openai"}, // NVIDIA, MoE, Fireworks (FW-)
	{Name: "Qwen3.6-35B-A3B", Protocol: "openai"},            // Alibaba, MoE, Fireworks (FW-)
	{Name: "Qwen3.6-27B", Protocol: "openai"},                // Alibaba, Dense* (moderate confidence), Fireworks (FW-)
	{Name: "Gemma-4-26B-A4B", Protocol: "openai"},            // Google, MoE, Fireworks (FW-)
	{Name: "Gemma-4-31B", Protocol: "openai"},                // Google, Dense, Fireworks (FW-)
	{Name: "Qwen3.5-397B-A17B", Protocol: "openai"},          // Alibaba, MoE, Fireworks (FW-)
}

func (c *azureConfigSource) Models(ctx context.Context) ([]ModelConfig, error) {
	raw := os.Getenv("AZURE_MODELS")
	if raw == "" {
		return defaultAzureModels, nil
	}
	var models []ModelConfig
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil, wrapErr("parsing AZURE_MODELS", err)
	}
	return models, nil
}

func (c *azureConfigSource) RescoreEveryRun() bool {
	if raw := os.Getenv("AZURE_RESCORE_EVERY_RUN"); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			return v
		}
	}
	return true
}

// Temperature is a single run-level value applied to every model in this run
// (CLAUDE.md §4.7) - not swept yet, just no longer confounded per-model.
// Default 0 (deterministic) when unset/unparseable.
func (c *azureConfigSource) Temperature() float32 {
	if raw := os.Getenv("AZURE_TEMPERATURE"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 32); err == nil {
			return float32(v)
		}
	}
	return 0
}

// azureSweepStartLayout matches AZURE_SWEEP_START's expected form, e.g. "2026-07-21".
const azureSweepStartLayout = "2006-01-02"

// BatchSize is run-level and swept, not per-model (CLAUDE.md §4.4/§4.5): it's
// computed from the calendar via batchSizeForDay/dayIndex (batchsweep.go)
// rather than read as a plain env value, because the Container Apps Job fires
// on a fixed unattended daily cron - nobody is available to set an env
// correctly every day across the ~30-day window. AZURE_BATCH_SIZE, if set,
// overrides the sweep entirely; that's a manual escape hatch for local
// testing/debugging, not part of the rotation design.
func (c *azureConfigSource) BatchSize() int {
	if raw := os.Getenv("AZURE_BATCH_SIZE"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			return v
		}
	}
	raw := os.Getenv("AZURE_SWEEP_START")
	start, err := time.Parse(azureSweepStartLayout, raw)
	if err != nil {
		// Unset/unparseable AZURE_SWEEP_START: degrade to the safest size
		// (smallest batch, least likely to blow a throttle budget) rather
		// than crash the run.
		return batchSizeForDay(0)
	}
	return batchSizeForDay(dayIndex(start, time.Now()))
}

// ContributorID/ResumeID/ConfigID are per-event identity (CLAUDE.md §10).
// Plain env reads, no parsing or defaults - empty means unset, and
// handler()'s gate turns that into a refusal to run rather than silently
// recording rows with no way to separate person-effects from model-effects.
func (c *azureConfigSource) ContributorID() string {
	return os.Getenv("AZURE_CONTRIBUTOR_ID")
}

func (c *azureConfigSource) ResumeID() string {
	return os.Getenv("AZURE_RESUME_ID")
}

func (c *azureConfigSource) ConfigID() string {
	return os.Getenv("AZURE_CONFIG_ID")
}
