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

// ScoreBand is one band of the screening-score range (0-100, CLAUDE.md's
// fixed-panel task) used to stratify panel selection. Bands are expected to
// partition the range as half-open [Min,Max), except the band with the
// greatest Min, which is treated as Max-inclusive so a perfect top score
// isn't dropped.
type ScoreBand struct {
	Name string  `json:"name"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

// BandTarget is the desired slot count for one ScoreBand.Name in a panel
// build, plus a guaranteed-minimum Floor (Floor <= Target). CLAUDE.md's "floor
// the top band" is expressed this way rather than hardcoding which band is
// "top" in code - the top band is just whichever ScoreBand has the greatest
// Min.
type BandTarget struct {
	Band   string `json:"band"` // must match a ScoreBand.Name
	Target int    `json:"target"`
	Floor  int    `json:"floor"`
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
	// ContributorID is per-event identity (CLAUDE.md §10): without it,
	// person-effects and model-effects are inseparable once data from more
	// than one contributor exists. Azure-only - AWS implementations return
	// "". ResumeID/ConfigID are not ConfigSource methods: both are content
	// hashes computed once in wireAzure (ResumeID reuses InstructionsVersion;
	// ConfigID is computeConfigID's output) and stored directly on App, the
	// same treatment InstructionsVersion already gets - a computed hash has
	// no env var to read, so there's nothing for a ConfigSource method to do.
	ContributorID() string
	// RunMode selects the run type: "main" (full set, current behavior) or
	// "floor" (representative panel, repeated). Only "main" is implemented;
	// "floor" is a later edition and the handler refuses it for now.
	RunMode() string
	// Panel knobs (CLAUDE.md's fixed score-stratified 30-job panel). This is
	// an Azure-only measurement-instrument concept - AWS implementations
	// return the zero value/false for every one of these, same convention as
	// RunMode()/ContributorID().
	PanelEnabled() bool
	PanelSize() int
	ScreeningModel() string
	ScoreBands() ([]ScoreBand, error)
	BandTargets() ([]BandTarget, error)
	PanelSeed() int64      // 0 means unset; build path refuses to build without a nonzero seed
	MaxPerCompany() int    // 0 means no cap
	ActivePanelID() string // "" means auto-detect (most-recently built panel)
	RebuildPanel() bool
}
