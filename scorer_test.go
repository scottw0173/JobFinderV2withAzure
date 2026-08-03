package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestZipScoreEventsDropsUnmatchedJob(t *testing.T) {
	var buf bytes.Buffer
	a := &App{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	jobs := []Job{{Key: "a"}, {Key: "b"}}
	results := []ScoreResult{{JobKey: "a", EmittedScore: 5}}

	events := zipScoreEvents(a, jobs, results, 0)

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

func TestZipScoreEventsPairsLeftoverAsSentinel(t *testing.T) {
	var buf bytes.Buffer
	a := &App{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	badRaw := json.RawMessage(`{"key":"bogus","score":99}`)
	ev := 42.0
	jobs := []Job{{Key: "a"}, {Key: "b"}}
	results := []ScoreResult{
		{JobKey: "a", EmittedScore: 5},
		{JobKey: "bogus", EmittedScore: 99, EVScore: &ev, Raw: badRaw, Model: "gpt-test"},
	}

	events := zipScoreEvents(a, jobs, results, 0)

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Job.Key != "a" || events[0].Result.EmittedScore != 5 {
		t.Fatalf("job a's event changed unexpectedly: %+v", events[0])
	}

	sentinel := events[1]
	if sentinel.Job.Key != "b" {
		t.Fatalf("got sentinel job key %q, want %q", sentinel.Job.Key, "b")
	}
	if sentinel.Result.EmittedScore != malformedKeyEmittedScore {
		t.Fatalf("got EmittedScore %v, want %v", sentinel.Result.EmittedScore, malformedKeyEmittedScore)
	}
	if sentinel.Result.EVScore != nil {
		t.Fatalf("got EVScore %v, want nil", *sentinel.Result.EVScore)
	}
	if string(sentinel.Result.Raw) != string(badRaw) {
		t.Fatalf("got Raw %s, want %s", sentinel.Result.Raw, badRaw)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"key":"b"`)) {
		t.Fatalf("expected a warning log referencing job b, got: %s", buf.String())
	}
}

func TestZipScoreEventsPairsUnattributedEmptyKeyResult(t *testing.T) {
	var buf bytes.Buffer
	a := &App{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	badRaw := json.RawMessage(`{"score":50}`)
	jobs := []Job{{Key: "x"}}
	results := []ScoreResult{{JobKey: "", Raw: badRaw, Reasoning: "salvaged"}}

	events := zipScoreEvents(a, jobs, results, 0)

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Job.Key != "x" {
		t.Fatalf("got job key %q, want %q", events[0].Job.Key, "x")
	}
	if events[0].Result.EmittedScore != malformedKeyEmittedScore {
		t.Fatalf("got EmittedScore %v, want %v", events[0].Result.EmittedScore, malformedKeyEmittedScore)
	}
	if string(events[0].Result.Raw) != string(badRaw) {
		t.Fatalf("got Raw %s, want %s", events[0].Result.Raw, badRaw)
	}
	if events[0].Result.Reasoning != "salvaged" {
		t.Fatalf("got Reasoning %q, want %q", events[0].Result.Reasoning, "salvaged")
	}
}

func TestZipScoreEventsPositionalPairingOrder(t *testing.T) {
	a := &App{Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))}

	rawFirst := json.RawMessage(`{"key":"bad1"}`)
	rawSecond := json.RawMessage(`{"key":"bad2"}`)
	jobs := []Job{{Key: "j1"}, {Key: "j2"}}
	results := []ScoreResult{
		{JobKey: "bad1", Raw: rawFirst},
		{JobKey: "bad2", Raw: rawSecond},
	}

	events := zipScoreEvents(a, jobs, results, 0)

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Job.Key != "j1" || string(events[0].Result.Raw) != string(rawFirst) {
		t.Fatalf("expected j1 paired with rawFirst, got job %q raw %s", events[0].Job.Key, events[0].Result.Raw)
	}
	if events[1].Job.Key != "j2" || string(events[1].Result.Raw) != string(rawSecond) {
		t.Fatalf("expected j2 paired with rawSecond, got job %q raw %s", events[1].Job.Key, events[1].Result.Raw)
	}
}

func TestZipScoreEventsExcessUnmatchedJobsStillDropped(t *testing.T) {
	var buf bytes.Buffer
	a := &App{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	rawLeftover := json.RawMessage(`{"key":"bad"}`)
	jobs := []Job{{Key: "j1"}, {Key: "j2"}}
	results := []ScoreResult{{JobKey: "bad", Raw: rawLeftover}}

	events := zipScoreEvents(a, jobs, results, 0)

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Job.Key != "j1" {
		t.Fatalf("expected j1 to be paired with the one leftover, got %q", events[0].Job.Key)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"key":"j2"`)) {
		t.Fatalf("expected a genuine-drop warning log for j2, got: %s", buf.String())
	}
}
