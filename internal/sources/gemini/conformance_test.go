package gemini

import (
	"os"
	"path/filepath"
	"testing"
)

// A messages-array line in the middle of a JSONL file must not wipe earlier
// lines' entries — the old code reset p.entries and then sliced
// p.entries[before:] past the end (panic) or truncated silently.
func TestMessagesArrayMidJSONLKeepsEarlierLines(t *testing.T) {
	direct := `{"type":"gemini","id":"d1","model":"gemini-2.5-pro",` +
		`"timestamp":"2025-07-01T10:00:00Z","tokens":{"input":100,"output":10}}`
	sessionDoc := `{"sessionId":"whole-doc","messages":[` +
		`{"type":"gemini","model":"gemini-2.5-flash","timestamp":"2025-07-01T11:00:00Z",` +
		`"tokens":{"input":50,"output":5}}]}`
	content := direct + "\n" + sessionDoc + "\n"
	path := writeLog(t, t.TempDir(), "chat-mixed.jsonl", content)
	entries := parseFile(path)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries (direct line + message row), got %d: %+v", len(entries), entries)
	}
	if entries[0].Model != "gemini-2.5-pro" || entries[0].SessionID != "chat-mixed" {
		t.Errorf("first entry wrong: %+v", entries[0])
	}
	if entries[1].Model != "gemini-2.5-flash" || entries[1].SessionID != "whole-doc" {
		t.Errorf("message entry wrong: %+v", entries[1])
	}
}

// Upstream compares the raw type string exactly: a padded value must not
// match the direct-event branch, and a non-string type falls through to
// stats parsing. xusage keeps its documented lenient superset, so the padded
// line still emits through the bare-tokens fallback.
func TestTypeDiscriminatorIsExactAndNonStringFallsThrough(t *testing.T) {
	padded := `{"type":" gemini ","tokens":{"input":10,"output":1},"timestamp":"2025-07-01T10:00:00Z"}`
	numeric := `{"type":5,"stats":{"models":{"m":{"tokens":{"input":10,"output":20,"total":30}}}},` +
		`"timestamp":"2025-07-01T10:00:00Z"}`
	path := writeLog(t, t.TempDir(), "chat-types.jsonl", padded+"\n"+numeric+"\n")
	entries := parseFile(path)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries (lenient padded row + stats row), got %d: %+v", len(entries), entries)
	}
	if entries[0].Model != "unknown" || entries[0].InputTokens != 10 {
		t.Errorf("padded row must take the bare-tokens fallback, got %+v", entries[0])
	}
	if entries[1].Model != "m" || entries[1].TotalTokens() != 30 {
		t.Errorf("stats record wrong: %+v", entries[1])
	}
}

// An explicit total of 0 is indistinguishable from an absent one in xusage
// (u64 coercion, documented pragmatic divergence from upstream Option<u64>):
// cached sits beside input either way.
func TestExplicitZeroTotalCountsAsPresent(t *testing.T) {
	line := `{"sessionId":"s","timestamp":"2025-07-01T10:00:00Z",` +
		`"tokens":{"input":0,"output":0,"cached":40,"total":0}}`
	path := writeLog(t, t.TempDir(), "chat-zero.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.InputTokens != 0 || e.CacheReadTokens != 40 {
		t.Errorf("split = (%d, read %d), want (0, 40)", e.InputTokens, e.CacheReadTokens)
	}
}

// Absent total means "unknown": the bare-tokens fallback applies overlap
// semantics (documented lenient superset), so cached is carved from input.
func TestAbsentTotalKeepsCachedBesideInput(t *testing.T) {
	line := `{"sessionId":"s","timestamp":"2025-07-01T10:00:00Z",` +
		`"tokens":{"input":10,"output":2,"cached":40}}`
	path := writeLog(t, t.TempDir(), "chat-nototal.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.InputTokens != 0 || e.CacheReadTokens != 40 {
		t.Errorf("split = (%d, read %d), want overlap (0, 40)", e.InputTokens, e.CacheReadTokens)
	}
}

// A re-emitted direct event replaces its earlier copy by id instead of
// stacking up (upstream direct_event_indexes).
func TestDirectEventIdReplacement(t *testing.T) {
	first := `{"type":"gemini","id":"same","model":"gemini-2.5-pro","tokens":{"input":10,"output":1}}`
	second := `{"type":"gemini","id":"same","model":"gemini-2.5-pro","tokens":{"input":20,"output":2}}`
	path := writeLog(t, t.TempDir(), "chat-repl.jsonl", first+"\n"+second+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 replaced entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].InputTokens != 20 || entries[0].OutputTokens != 2 {
		t.Errorf("replacement did not win: %+v", entries[0])
	}
}

// Missing files yield nil, never an error or panic.
func TestParseMissingFileYieldsNil(t *testing.T) {
	if entries := parseFile(filepath.Join(t.TempDir(), "nope.json")); entries != nil {
		t.Fatalf("want nil, got %+v", entries)
	}
}

// GEMINI_DATA_DIR env override wins entirely over the home default
// (upstream paths(): early return when the env var is set).
func TestEnvOverrideWinsEntirely(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	defaultDir := filepath.Join(home, ".gemini", "tmp")
	writeLog(t, defaultDir, "default.jsonl", "{}\n")
	envDir := t.TempDir()
	writeLog(t, envDir, "env.jsonl", "{}\n")
	t.Setenv("GEMINI_DATA_DIR", envDir)

	files := discoverLogFiles()
	if len(files) != 1 || filepath.Base(files[0]) != "env.jsonl" {
		t.Fatalf("discovery = %v, want only the env dir file", files)
	}
	os.RemoveAll(envDir)
	if files := discoverLogFiles(); len(files) != 0 {
		t.Fatalf("missing env dir must yield empty discovery, got %v", files)
	}
}
