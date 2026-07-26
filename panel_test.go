package main

import (
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"testing"
)

// bucketByBand logs via the package-level app var when a job has no
// screening score (same convention as aiErrors&Throttling.go's
// tpmThrottle.reserve) - give it a discard logger so tests exercising that
// path don't nil-panic.
func TestMain(m *testing.M) {
	app = &App{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	os.Exit(m.Run())
}

func mkJob(key, company, title string, postedAt int64) Job {
	return Job{Key: key, Title: title, Company: company, Location: "Remote", PostedAt: postedAt}
}

func makeBucket(n int, companies []string, band string, score float64) []screenedJob {
	out := make([]screenedJob, 0, n)
	for i := 0; i < n; i++ {
		c := companies[i%len(companies)]
		key := fmt.Sprintf("%s-%s-%d", band, c, i)
		out = append(out, screenedJob{Job: mkJob(key, c, fmt.Sprintf("Role%d", i), int64(i)), Score: score})
	}
	return out
}

func stablekeysOf(sel []screenedJob) []string {
	out := make([]string, len(sel))
	for i, sj := range sel {
		out[i] = sj.Job.createStableKey()
	}
	sort.Strings(out)
	return out
}

func testPanelInputs() (map[string][]screenedJob, []ScoreBand, []BandTarget) {
	buckets := map[string][]screenedJob{
		"low":  makeBucket(20, []string{"A", "B", "C", "D"}, "low", 10),
		"high": makeBucket(18, []string{"A", "B", "C"}, "high", 90),
	}
	bands := []ScoreBand{{Name: "low", Min: 0, Max: 50}, {Name: "high", Min: 50, Max: 100}}
	targets := []BandTarget{{Band: "low", Target: 4, Floor: 0}, {Band: "high", Target: 6, Floor: 6}}
	return buckets, bands, targets
}

func TestSortBands(t *testing.T) {
	in := []ScoreBand{
		{Name: "b", Min: 50, Max: 100},
		{Name: "a", Min: 0, Max: 50},
	}
	out := sortBands(in)
	if out[0].Name != "a" || out[1].Name != "b" {
		t.Fatalf("sortBands() = %+v, want ascending by Min", out)
	}
	if in[0].Name != "b" {
		t.Fatal("sortBands should not mutate its input")
	}
}

func TestBandFor(t *testing.T) {
	bands := sortBands([]ScoreBand{
		{Name: "low", Min: 0, Max: 40},
		{Name: "mid", Min: 40, Max: 80},
		{Name: "high", Min: 80, Max: 100},
	})
	tests := []struct {
		score float64
		want  string
	}{
		{0, "low"},
		{39.9, "low"},
		{40, "mid"},
		{79.9, "mid"},
		{80, "high"},
		{100, "high"}, // top band is Max-inclusive
	}
	for _, tt := range tests {
		got, ok := bandFor(tt.score, bands)
		if !ok || got.Name != tt.want {
			t.Errorf("bandFor(%v) = %q, %v, want %q", tt.score, got.Name, ok, tt.want)
		}
	}
	if _, ok := bandFor(150, bands); ok {
		t.Error("bandFor(150) should not match any configured band")
	}
}

func TestBucketByBandDedupAndMissingScore(t *testing.T) {
	jobs := []Job{
		mkJob("k1", "Acme", "Engineer", 100),
		mkJob("k1dup", "Acme", "Engineer", 200), // same stablekey, later PostedAt should win
		mkJob("k2", "Other", "Engineer", 50),    // no screening score - must be excluded
	}
	scores := map[string]float64{"k1": 90, "k1dup": 91}
	bands := []ScoreBand{{Name: "high", Min: 0, Max: 100}}

	buckets, err := bucketByBand(jobs, scores, bands)
	if err != nil {
		t.Fatalf("bucketByBand() error: %v", err)
	}
	got := buckets["high"]
	if len(got) != 1 {
		t.Fatalf("bucketByBand() = %d jobs, want 1 (dedup collision + exclude no-score job)", len(got))
	}
	if got[0].Job.Key != "k1dup" {
		t.Fatalf("bucketByBand() kept Key %q, want the later-PostedAt duplicate (k1dup)", got[0].Job.Key)
	}
}

func TestBucketByBandScoreOutsideAllBands(t *testing.T) {
	jobs := []Job{mkJob("k1", "Acme", "Engineer", 1)}
	scores := map[string]float64{"k1": 150}
	bands := []ScoreBand{{Name: "only", Min: 0, Max: 100}}
	if _, err := bucketByBand(jobs, scores, bands); err == nil {
		t.Fatal("bucketByBand() should error when a screening score falls outside every configured band")
	}
}

func TestSelectPanelDeterministic(t *testing.T) {
	buckets, bands, targets := testPanelInputs()
	sel1, err := selectPanel(buckets, bands, targets, 10, 0, 42)
	if err != nil {
		t.Fatalf("selectPanel() error: %v", err)
	}
	sel2, err := selectPanel(buckets, bands, targets, 10, 0, 42)
	if err != nil {
		t.Fatalf("selectPanel() error: %v", err)
	}
	if !reflect.DeepEqual(stablekeysOf(sel1), stablekeysOf(sel2)) {
		t.Fatalf("same seed produced different selections:\n%v\n%v", stablekeysOf(sel1), stablekeysOf(sel2))
	}
}

func TestSelectPanelSeedVariance(t *testing.T) {
	buckets, bands, targets := testPanelInputs()
	sel1, err := selectPanel(buckets, bands, targets, 10, 0, 1)
	if err != nil {
		t.Fatalf("selectPanel() error: %v", err)
	}
	sel2, err := selectPanel(buckets, bands, targets, 10, 0, 2)
	if err != nil {
		t.Fatalf("selectPanel() error: %v", err)
	}
	if reflect.DeepEqual(stablekeysOf(sel1), stablekeysOf(sel2)) {
		t.Fatal("different seeds produced identical selections - selection isn't actually seed-driven")
	}
}

func TestSelectPanelFloorHonored(t *testing.T) {
	buckets := map[string][]screenedJob{
		"low":  makeBucket(2, []string{"A", "B"}, "low", 10),
		"high": makeBucket(6, []string{"A", "B", "C"}, "high", 90),
	}
	bands := []ScoreBand{{Name: "low", Min: 0, Max: 50}, {Name: "high", Min: 50, Max: 100}}
	targets := []BandTarget{{Band: "low", Target: 8, Floor: 0}, {Band: "high", Target: 6, Floor: 6}}

	sel, err := selectPanel(buckets, bands, targets, 8, 0, 7)
	if err != nil {
		t.Fatalf("selectPanel() error: %v", err)
	}
	highCount := 0
	for _, sj := range sel {
		if sj.Score >= 50 {
			highCount++
		}
	}
	if highCount != 6 {
		t.Fatalf("top band floor not honored: got %d high-band jobs, want 6", highCount)
	}
}

func TestSelectPanelMaxPerCompany(t *testing.T) {
	buckets := map[string][]screenedJob{"band": makeBucket(20, []string{"A"}, "band", 50)}
	bands := []ScoreBand{{Name: "band", Min: 0, Max: 100}}
	targets := []BandTarget{{Band: "band", Target: 10, Floor: 0}}

	sel, err := selectPanel(buckets, bands, targets, 10, 2, 3)
	if err != nil {
		t.Fatalf("selectPanel() error: %v", err)
	}
	if len(sel) != 2 {
		t.Fatalf("selectPanel() with maxPerCompany=2 against a single-company pool returned %d jobs, want 2", len(sel))
	}
}

func TestRoundRobinTakeOrder(t *testing.T) {
	pool := []screenedJob{
		{Job: mkJob("A1", "A", "r1", 1), Score: 1},
		{Job: mkJob("A2", "A", "r2", 2), Score: 1},
		{Job: mkJob("B1", "B", "r1", 1), Score: 1},
	}
	selected := map[string]bool{}
	companyCounts := map[string]int{}
	rng := rand.New(rand.NewSource(1))

	out := roundRobinTake(pool, 3, companyCounts, 0, selected, rng)
	if len(out) != 3 {
		t.Fatalf("roundRobinTake() returned %d jobs, want 3 (pool fully consumable)", len(out))
	}

	posB, posA2 := -1, -1
	for i, sj := range out {
		if sj.Job.Company == "B" {
			posB = i
		}
		if sj.Job.Key == "A2" {
			posA2 = i
		}
	}
	if posB == -1 || posA2 == -1 {
		t.Fatal("expected both companies represented in the output")
	}
	if posB > posA2 {
		t.Fatalf("company B's 1st pick (pos %d) came after company A's 2nd pick (pos %d) - round robin broken", posB, posA2)
	}
}
