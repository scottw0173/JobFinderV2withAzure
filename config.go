package main

import "context"

type ModelConfig struct {
	Name         string
	Deployment   string
	WantLogprobs bool
	TPM          int    // tokens/minute quota; handler() refuses to score without a positive value (CLAUDE.md §8)
	RPM          int    // requests/minute quota; same requirement
	Protocol     string // which scorer to route to (CLAUDE.md §7), e.g. "openai"; handler() refuses to score without one
	BaseURL      string // per-model endpoint (CLAUDE.md §6/§12) - native Foundry and Fireworks-served deployments differ
	AuthScope    string // credential kind for this endpoint; structural only until keyless auth lands (§7/§9)
}

// ConfigSource resolves file-shaped app config (sources.json,
// filterKeywords.json, instructions.md) plus the knobs that are
// configuration, never forked code: rescore policy, model list, and
// temperature. Temperature is run-level, not per-model (CLAUDE.md §4.7) - a
// variable compared across models must be held constant across the
// comparison, same reasoning as batch size.
type ConfigSource interface {
	File(ctx context.Context, name string) ([]byte, error)
	Models(ctx context.Context) ([]ModelConfig, error)
	RescoreEveryRun() bool
	Temperature() float32
	BatchSize() int
	// ContributorID/ResumeID/ConfigID are per-event identity (CLAUDE.md §10):
	// without them, person-effects and model-effects are inseparable once
	// data from more than one contributor exists. Azure-only - AWS
	// implementations return "".
	ContributorID() string
	ResumeID() string
	ConfigID() string
	// RunMode selects the run type: "main" (full set, current behavior) or
	// "floor" (representative panel, repeated). Only "main" is implemented;
	// "floor" is a later edition and the handler refuses it for now.
	RunMode() string
}
