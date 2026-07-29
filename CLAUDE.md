## Task: make the date+time panel-seed derivation testable

Context: panel.go `buildPanel` now derives a YYYYMMDDHHMMSS seed when
`AZURE_PANEL_SEED` is unset (0), instead of erroring. The derivation is
currently inline. Extract it to a pure helper so it can be unit-tested,
and fix the imports.

Scope: panel.go and panel_test.go only. Do NOT touch config_azure.go,
store_azure.go, the schema, or any Bicep. Behavior is decided — do not
propose alternatives.

### 1. Add the helper (panel.go)
Add near the top of panel.go, after the imports:

    // deriveSeed produces a date+time panel seed (YYYYMMDDHHMMSS, UTC) used
    // when AZURE_PANEL_SEED is unset, so each panel's seed itself carries a
    // traceable build timestamp. The Format output is always 14 ASCII digits
    // (max 99991231235959, well within int64), so the parse error is
    // unreachable and safe to discard.
    func deriveSeed(now time.Time) int64 {
        v, _ := strconv.ParseInt(now.UTC().Format("20060102150405"), 10, 64)
        return v
    }

### 2. Replace the inline block in buildPanel with a call
Change the seed-derivation block to:

    seed := app.Config.PanelSeed()
    if seed == 0 {
        seed = deriveSeed(time.Now())
        app.Logger.Info("no AZURE_PANEL_SEED set; derived date+time seed", "seed", seed)
    }


### 3. Add test (panel_test.go)
    func TestDeriveSeed(t *testing.T) {
        want := int64(20260729143005)
        utc := time.Date(2026, 7, 29, 14, 30, 5, 0, time.UTC)
        if got := deriveSeed(utc); got != want {
            t.Fatalf("deriveSeed(UTC) = %d, want %d", got, want)
        }
        // non-UTC input must normalize to UTC before formatting
        loc := time.FixedZone("MST", -7*3600)
        local := time.Date(2026, 7, 29, 7, 30, 5, 0, loc) // == 14:30:05 UTC
        if got := deriveSeed(local); got != want {
            t.Fatalf("deriveSeed(non-UTC) = %d, want %d", got, want)
        }
    }

### 4. CLAUDE.md
If there is an existing section covering the panel seed / reproducibility,
add one line: seed auto-derives from build date+time (YYYYMMDDHHMMSS, UTC)
when AZURE_PANEL_SEED is unset; the env var remains an optional override for
replay. Do NOT renumber any sections.

### 5. Verify
Run: gofmt -l ., go build ./..., go vet ./..., go test -race ./...
