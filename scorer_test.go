package main

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestZipScoreEventsDropsUnmatchedJob(t *testing.T) {
	var buf bytes.Buffer
	a := &App{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	jobs := []Job{{Key: "a"}, {Key: "b"}}
	results := []ScoreResult{{JobKey: "a", EmittedScore: 5}}

	events := zipScoreEvents(a, jobs, results)

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Job.Key != "a" {
		t.Fatalf("got job key %q, want %q", events[0].Job.Key, "a")
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"key":"b"`)) {
		t.Fatalf("expected a warning log for the unmatched job b, got: %s", buf.String())
	}
}
