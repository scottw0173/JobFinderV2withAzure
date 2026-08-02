package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// instructionsVersionHash is the content-hash primitive both
// instructions_version and config_id are built from (CLAUDE.md §10): sha256,
// hex-encoded, truncated to 12 chars. Extracted out of wireAzure so it has a
// name a regression test can pin against.
func instructionsVersionHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// configHashInput holds exactly the fields CLAUDE.md's config_id definition
// (v1) selects for hashing: the environment knobs that define whether two
// runs are comparable. Deliberately excludes anything already its own
// scoring_calls column (temperature_sent, batch_size, run_kind) and anything
// identity-derived (contributor_id, resume_id) - see CLAUDE.md.
type configHashInput struct {
	Sources        map[string][]string `json:"sources"`
	FilterRules    KeywordFilter       `json:"filter_rules"`
	ScreeningModel string              `json:"screening_model"`
	ScoreBands     []ScoreBand         `json:"score_bands"`
	BandTargets    []BandTarget        `json:"band_targets"`
	PanelSize      int                 `json:"panel_size"`
}

// computeConfigID hashes configHashInput into a config_id (CLAUDE.md's
// config_id definition, v1): sha256 of a canonicalized JSON encoding,
// truncated to 12 hex chars, prefixed "config-" to stay visually
// distinguishable from instructions_version alongside it in stored rows.
// Every collection (map values, slices) is sorted before marshaling so
// reordering entries in sources.json/filterKeywords.json/AZURE_SCORE_BANDS/
// AZURE_BAND_TARGETS - without changing their content - doesn't change the
// hash; json.Marshal already sorts Go map keys, so only the nested slices
// need explicit sorting.
func computeConfigID(ctx context.Context, cfg ConfigSource) (string, error) {
	sourcesRaw, err := cfg.File(ctx, "sources.json")
	if err != nil {
		return "", wrapErr("reading sources.json for config_id", err)
	}
	var sources map[string][]string
	if err := json.Unmarshal(sourcesRaw, &sources); err != nil {
		return "", wrapErr("parsing sources.json for config_id", err)
	}
	for k := range sources {
		sorted := append([]string(nil), sources[k]...)
		sort.Strings(sorted)
		sources[k] = sorted
	}

	filterRaw, err := cfg.File(ctx, "filterKeywords.json")
	if err != nil {
		return "", wrapErr("reading filterKeywords.json for config_id", err)
	}
	var filter KeywordFilter
	if err := json.Unmarshal(filterRaw, &filter); err != nil {
		return "", wrapErr("parsing filterKeywords.json for config_id", err)
	}
	filter.Include = sortedCopy(filter.Include)
	filter.Exclude = sortedCopy(filter.Exclude)

	bands, err := cfg.ScoreBands()
	if err != nil {
		return "", wrapErr("reading score bands for config_id", err)
	}
	bands = append([]ScoreBand(nil), bands...)
	sort.Slice(bands, func(i, j int) bool { return bands[i].Min < bands[j].Min })

	targets, err := cfg.BandTargets()
	if err != nil {
		return "", wrapErr("reading band targets for config_id", err)
	}
	targets = append([]BandTarget(nil), targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].Band < targets[j].Band })

	input := configHashInput{
		Sources:        sources,
		FilterRules:    filter,
		ScreeningModel: cfg.ScreeningModel(),
		ScoreBands:     bands,
		BandTargets:    targets,
		PanelSize:      cfg.PanelSize(),
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return "", wrapErr("marshaling config_id input", err)
	}
	sum := sha256.Sum256(payload)
	return "config-" + hex.EncodeToString(sum[:])[:12], nil
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
