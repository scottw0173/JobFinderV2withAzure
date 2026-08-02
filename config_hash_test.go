package main

import (
	"context"
	"testing"
)

// fakeConfigSource implements ConfigSource with just enough behavior for
// computeConfigID's test cases; every method computeConfigID doesn't touch
// returns a zero value.
type fakeConfigSource struct {
	files       map[string][]byte
	screening   string
	scoreBands  []ScoreBand
	bandTargets []BandTarget
	panelSize   int
}

func (f *fakeConfigSource) File(_ context.Context, name string) ([]byte, error) {
	return f.files[name], nil
}
func (f *fakeConfigSource) Models(context.Context) ([]ModelConfig, error) { return nil, nil }
func (f *fakeConfigSource) RescoreEveryRun() bool                         { return false }
func (f *fakeConfigSource) Temperature() float32                          { return 0 }
func (f *fakeConfigSource) BatchSize() int                                { return 0 }
func (f *fakeConfigSource) ContributorID() string                         { return "" }
func (f *fakeConfigSource) RunMode() string                               { return "main" }
func (f *fakeConfigSource) PanelEnabled() bool                            { return true }
func (f *fakeConfigSource) PanelSize() int                                { return f.panelSize }
func (f *fakeConfigSource) ScreeningModel() string                        { return f.screening }
func (f *fakeConfigSource) ScoreBands() ([]ScoreBand, error)              { return f.scoreBands, nil }
func (f *fakeConfigSource) BandTargets() ([]BandTarget, error)            { return f.bandTargets, nil }
func (f *fakeConfigSource) PanelSeed() int64                              { return 0 }
func (f *fakeConfigSource) MaxPerCompany() int                            { return 0 }
func (f *fakeConfigSource) ActivePanelID() string                         { return "" }
func (f *fakeConfigSource) RebuildPanel() bool                            { return false }

func baseFakeConfig() *fakeConfigSource {
	return &fakeConfigSource{
		files: map[string][]byte{
			"sources.json":        []byte(`{"greenhouse":["acme","globex"],"lever":["initech"]}`),
			"filterKeywords.json": []byte(`{"include":["golang","remote"],"exclude":["senior"]}`),
		},
		screening:   "gpt-5.4-mini",
		scoreBands:  []ScoreBand{{Name: "reject", Min: 0, Max: 40}, {Name: "viable", Min: 80, Max: 100}},
		bandTargets: []BandTarget{{Band: "reject", Target: 4, Floor: 0}, {Band: "viable", Target: 8, Floor: 8}},
		panelSize:   30,
	}
}

func TestComputeConfigIDPinnedValue(t *testing.T) {
	got, err := computeConfigID(context.Background(), baseFakeConfig())
	if err != nil {
		t.Fatalf("computeConfigID: %v", err)
	}
	const want = "config-b35d986f6ef0"
	if got != want {
		t.Fatalf("computeConfigID() = %q, want %q (pinned regression value - if this legitimately changed, CLAUDE.md's config_id v1 definition changed too and needs a version bump, not a silent edit)", got, want)
	}
}

func TestComputeConfigIDStableUnderReordering(t *testing.T) {
	base := baseFakeConfig()
	baseID, err := computeConfigID(context.Background(), base)
	if err != nil {
		t.Fatalf("computeConfigID: %v", err)
	}

	reordered := baseFakeConfig()
	reordered.files["sources.json"] = []byte(`{"lever":["initech"],"greenhouse":["globex","acme"]}`)
	reordered.files["filterKeywords.json"] = []byte(`{"exclude":["senior"],"include":["remote","golang"]}`)
	reordered.scoreBands = []ScoreBand{{Name: "viable", Min: 80, Max: 100}, {Name: "reject", Min: 0, Max: 40}}
	reordered.bandTargets = []BandTarget{{Band: "viable", Target: 8, Floor: 8}, {Band: "reject", Target: 4, Floor: 0}}

	reorderedID, err := computeConfigID(context.Background(), reordered)
	if err != nil {
		t.Fatalf("computeConfigID: %v", err)
	}
	if baseID != reorderedID {
		t.Fatalf("reordering keys/slices changed the hash: base=%q reordered=%q, want identical", baseID, reorderedID)
	}
}

func TestComputeConfigIDChangesOnEachField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakeConfigSource)
	}{
		{"sources", func(f *fakeConfigSource) { f.files["sources.json"] = []byte(`{"greenhouse":["acme"]}`) }},
		{"filter rules", func(f *fakeConfigSource) {
			f.files["filterKeywords.json"] = []byte(`{"include":["golang"],"exclude":[]}`)
		}},
		{"screening model", func(f *fakeConfigSource) { f.screening = "gpt-5.6-sol" }},
		{"score bands", func(f *fakeConfigSource) {
			f.scoreBands = append(f.scoreBands, ScoreBand{Name: "mid", Min: 40, Max: 80})
		}},
		{"band targets", func(f *fakeConfigSource) {
			f.bandTargets = append(f.bandTargets, BandTarget{Band: "mid", Target: 18, Floor: 0})
		}},
		{"panel size", func(f *fakeConfigSource) { f.panelSize = 20 }},
	}

	base := baseFakeConfig()
	baseID, err := computeConfigID(context.Background(), base)
	if err != nil {
		t.Fatalf("computeConfigID: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mutated := baseFakeConfig()
			c.mutate(mutated)
			mutatedID, err := computeConfigID(context.Background(), mutated)
			if err != nil {
				t.Fatalf("computeConfigID: %v", err)
			}
			if mutatedID == baseID {
				t.Fatalf("changing %s did not change config_id (%q)", c.name, mutatedID)
			}
		})
	}
}

// TestInstructionsVersionHash pins the instructions_version computation
// (sha256(instructions.md content), hex, truncated to 12 chars, main.go's
// wireAzure) against a known input, so this task's changes elsewhere in
// wireAzure can be verified not to have disturbed it - no such regression
// test existed before.
func TestInstructionsVersionHash(t *testing.T) {
	got := instructionsVersionHash([]byte("score candidates strictly on technical fit"))
	const want = "235f9150e288"
	if got != want {
		t.Fatalf("instructionsVersionHash() = %q, want %q (pinned regression value)", got, want)
	}
}
