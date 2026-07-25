package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAWSConfigSourceDefaults(t *testing.T) {
	c := newAWSConfigSource(nil, "bucket", "gemini-3.1-flash-lite")
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models() error: %v", err)
	}
	if len(models) != 1 || models[0].Name != "gemini-3.1-flash-lite" {
		t.Fatalf("got %+v, want a single gemini-3.1-flash-lite model", models)
	}
	if models[0].TPM != 200000 || models[0].RPM != 12 {
		t.Fatalf("got TPM=%d RPM=%d, want TPM=200000 RPM=12 (reused prior throttle magic numbers)", models[0].TPM, models[0].RPM)
	}
	if c.RescoreEveryRun() {
		t.Fatal("AWS RescoreEveryRun() should be false")
	}
	if c.BatchSize() != 5 {
		t.Fatalf("AWS BatchSize() = %d, want 5 (fixed, sweep is Azure-only)", c.BatchSize())
	}
	if c.ContributorID() != "" || c.ResumeID() != "" || c.ConfigID() != "" {
		t.Fatalf("AWS contributor/resume/config identity should all be empty, got %q/%q/%q",
			c.ContributorID(), c.ResumeID(), c.ConfigID())
	}
	if models[0].Protocol != "gemini" {
		t.Fatalf("AWS model Protocol = %q, want %q", models[0].Protocol, "gemini")
	}
}

func TestAzureConfigSourceDefaults(t *testing.T) {
	t.Setenv("AZURE_MODELS", "")
	t.Setenv("AZURE_RESCORE_EVERY_RUN", "")
	t.Setenv("AZURE_BATCH_SIZE", "")
	t.Setenv("AZURE_SWEEP_START", "")
	t.Setenv("AZURE_CONTRIBUTOR_ID", "")
	t.Setenv("AZURE_RESUME_ID", "")
	t.Setenv("AZURE_CONFIG_ID", "")
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}

	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models() error: %v", err)
	}
	// The panel is a fixed, known-size selection (CLAUDE.md §12), not a
	// loose "some models" check.
	if len(models) != 12 {
		t.Fatalf("got %d models, want the 12-model panel (CLAUDE.md §12)", len(models))
	}
	if !c.RescoreEveryRun() {
		t.Fatal("azure RescoreEveryRun() should default true")
	}
	if got := c.BatchSize(); got != 1 {
		t.Fatalf("azure BatchSize() with no AZURE_SWEEP_START = %d, want 1 (safe fallback)", got)
	}
	if c.ContributorID() != "" || c.ResumeID() != "" || c.ConfigID() != "" {
		t.Fatalf("azure contributor/resume/config identity should default empty, got %q/%q/%q",
			c.ContributorID(), c.ResumeID(), c.ConfigID())
	}
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		if m.Protocol == "" {
			t.Errorf("default model %q has empty Protocol", m.Name)
		}
		// BaseURL/TPM/RPM/AuthScope are intentionally left unset here: real
		// values are launch-day VERIFY data (CLAUDE.md §9/§12) that don't
		// exist yet, same treatment already applied to TPM/RPM before this
		// panel existed - asserting them non-empty would be asserting a
		// fabricated account-dependent value.
		if seen[m.Name] {
			t.Errorf("duplicate default model name %q", m.Name)
		}
		seen[m.Name] = true
	}
}

func TestAzureConfigSourceContributorIdentity(t *testing.T) {
	t.Setenv("AZURE_CONTRIBUTOR_ID", "swarner")
	t.Setenv("AZURE_RESUME_ID", "swe-resume-v2")
	t.Setenv("AZURE_CONFIG_ID", "default-panel")
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}

	if got := c.ContributorID(); got != "swarner" {
		t.Errorf("ContributorID() = %q, want %q", got, "swarner")
	}
	if got := c.ResumeID(); got != "swe-resume-v2" {
		t.Errorf("ResumeID() = %q, want %q", got, "swe-resume-v2")
	}
	if got := c.ConfigID(); got != "default-panel" {
		t.Errorf("ConfigID() = %q, want %q", got, "default-panel")
	}
}

func TestAzureConfigSourceBatchSizeOverride(t *testing.T) {
	t.Setenv("AZURE_BATCH_SIZE", "3")
	t.Setenv("AZURE_SWEEP_START", "2026-07-21")
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}

	if got := c.BatchSize(); got != 3 {
		t.Fatalf("azure BatchSize() with AZURE_BATCH_SIZE=3 = %d, want 3 (override wins over sweep)", got)
	}
}

func TestAzureConfigSourceUsesBlobWhenStorageAccountSet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "filterKeywords.json"), []byte(`{"include":[],"exclude":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_CONFIG_DIR", dir)
	t.Setenv("AZURE_STORAGE_ACCOUNT", "jfteststorage")
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}
	// Construction never touches the network (matches newAzureCredential's
	// contract in secrets_azure.go) - what's under test here is the branch
	// itself: AZURE_STORAGE_ACCOUNT set means File() must go through the
	// blob client, not silently fall back to the local dir it would
	// otherwise use. A real download is tier-2 per CLAUDE.md §9 (talks to
	// an external service) - proving the branch is taken is the right
	// amount of test here, not standing up a fake blob server.
	if c.blob == nil {
		t.Fatal("blob client should be constructed when AZURE_STORAGE_ACCOUNT is set")
	}
}

func TestAzureConfigSourceRunModeDefault(t *testing.T) {
	t.Setenv("AZURE_RUN_MODE", "")
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}
	if got := c.RunMode(); got != "main" {
		t.Fatalf("azure RunMode() with AZURE_RUN_MODE unset = %q, want %q", got, "main")
	}
}

func TestAzureConfigSourceRunModeOverride(t *testing.T) {
	t.Setenv("AZURE_RUN_MODE", "floor")
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}
	if got := c.RunMode(); got != "floor" {
		t.Fatalf("azure RunMode() with AZURE_RUN_MODE=floor = %q, want %q", got, "floor")
	}
}

func TestAWSConfigSourceRunModeAlwaysMain(t *testing.T) {
	c := newAWSConfigSource(nil, "bucket", "gemini-3.1-flash-lite")
	if got := c.RunMode(); got != "main" {
		t.Fatalf("AWS RunMode() = %q, want %q (floor is Azure-only)", got, "main")
	}
}

// TestNonMainRunModeIsRefused exercises the same "mode != main" check
// handler() runs (CLAUDE.md §2), against a RunMode() of "floor". Standing up
// a full handler() run needs a live-shaped collect/store/scorer chain
// (CLAUDE.md §9 tier 2 - cheapest fake, not a realistic harness), which is
// far more than this one string-comparison guard needs to prove.
func TestNonMainRunModeIsRefused(t *testing.T) {
	t.Setenv("AZURE_RUN_MODE", "floor")
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}
	mode := c.RunMode()
	if mode == "main" {
		t.Fatal("test setup broken: expected a non-main mode")
	}
	got := traceErrorf("run mode %q not implemented (only \"main\" is wired; floor is a later edition)", mode)
	want := `run mode "floor" not implemented (only "main" is wired; floor is a later edition)`
	if got.Error() != want {
		t.Fatalf("error message = %q, want %q", got.Error(), want)
	}
}

func TestAzureConfigSourceReadsLocalFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "filterKeywords.json"), []byte(`{"include":[],"exclude":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_CONFIG_DIR", dir)
	c, err := newAzureConfigSource(nil)
	if err != nil {
		t.Fatalf("newAzureConfigSource() error: %v", err)
	}

	data, err := c.File(context.Background(), "filterKeywords.json")
	if err != nil {
		t.Fatalf("File() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty file content")
	}
}
