package cline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/model"
)

func writeTranscript(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustParseFile(t *testing.T, path string) []model.Entry {
	t.Helper()
	entries, err := parseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestParseHappyPath(t *testing.T) {
	dir := t.TempDir()
	line := `{"type":"assistant","timestamp":"2026-03-29T07:00:00.000Z","sessionId":"sess-1",` +
		`"message":{"id":"msg-1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":1000,"output_tokens":200,` +
		`"cache_creation_input_tokens":100,"cache_read_input_tokens":50}},"costUSD":0.03}`
	p := writeTranscript(t, dir, "task.jsonl", line+"\n")
	entries := mustParseFile(t, p)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "claude-sonnet-4-20250514" || e.SessionID != "sess-1" || e.ID != "msg-1" {
		t.Errorf("identity wrong: %+v", e)
	}
	// Claude-style: input and cache-read are disjoint (no carving).
	if e.InputTokens != 1000 || e.CacheReadTokens != 50 {
		t.Errorf("input/cache wrong: input=%d cache=%d", e.InputTokens, e.CacheReadTokens)
	}
	if e.OutputTokens != 200 || e.CacheCreationTokens != 100 {
		t.Errorf("tokens wrong: out=%d cc=%d", e.OutputTokens, e.CacheCreationTokens)
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.03 {
		t.Errorf("cost wrong: %v", e.ReportedCost)
	}
	want := time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC)
	if !e.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", e.Timestamp, want)
	}
}

func TestParseAlternateSpellings(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","message":{"model":"claude-opus-4-20250514",` +
		`"usage":{"prompt_tokens":500,"completion_tokens":80}}}`
	p := writeTranscript(t, dir, "task.jsonl", line+"\n")
	entries := mustParseFile(t, p)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.InputTokens != 500 || e.OutputTokens != 80 {
		t.Errorf("spelling normalize wrong: in=%d out=%d", e.InputTokens, e.OutputTokens)
	}
}

func TestParseMalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	content := "garbage\n{bad\n" +
		`{"timestamp":"2026-03-29T07:00:00.000Z","message":{"id":"m","model":"claude-sonnet-4-20250514",` +
		`"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	p := writeTranscript(t, dir, "task.jsonl", content)
	entries := mustParseFile(t, p)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
}

func TestParseMissingUsageSkipped(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","message":{"id":"m","model":"claude-sonnet-4-20250514","content":"hi"}}`
	p := writeTranscript(t, dir, "task.jsonl", line+"\n")
	if entries := mustParseFile(t, p); len(entries) != 0 {
		t.Fatalf("want 0 entries, got %d", len(entries))
	}
}

func TestDiscoverEnvOverride(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "task.jsonl",
		`{"timestamp":"2026-03-29T07:00:00.000Z","message":{"id":"m","model":"claude-sonnet-4-20250514",`+
			`"usage":{"input_tokens":1}}}`+"\n")
	t.Setenv("CLINE_HOME", dir)
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d: %v", len(files), files)
	}
}

func TestDiscoverMissingDirReturnsNil(t *testing.T) {
	t.Setenv("CLINE_HOME", filepath.Join(t.TempDir(), "missing"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover error = %v, want nil", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}
