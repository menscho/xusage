package gemini

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir, name, content string) string {
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

func TestParseJSONLHappyPath(t *testing.T) {
	line := `{"timestamp":"2025-07-01T10:00:00Z","sessionId":"g-sess","model":"gemini-2.5-pro",` +
		`"tokens":{"input":1000,"output":200,"cached":400,"thoughts":50,"total":1650}}`
	path := writeLog(t, t.TempDir(), "chat-a.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "gemini-2.5-pro" || e.SessionID != "g-sess" {
		t.Errorf("bad identity: %+v", e)
	}
	// Cached (400) splits off the prompt: input 600, cache read 400.
	if e.InputTokens != 600 || e.CacheReadTokens != 400 {
		t.Errorf("input/cache split wrong: %d/%d", e.InputTokens, e.CacheReadTokens)
	}
	if e.OutputTokens != 250 { // 200 + 50 thoughts
		t.Errorf("output = %d, want 250", e.OutputTokens)
	}
	if e.TotalTokens() != 1650 {
		t.Errorf("total = %d, want 1650", e.TotalTokens())
	}
}

func TestParseWholeFileJSON(t *testing.T) {
	// Upstream parse_json_file: a messages array short-circuits the document —
	// only per-message events count, and a stats block beside it is ignored.
	doc := `{"sessionId":"whole","startTime":"2025-07-01T09:00:00Z",` +
		`"messages":[{"type":"gemini","model":"gemini-2.5-flash","timestamp":"2025-07-01T09:05:00Z","tokens":{"input":300,"output":55,"total":355}}],` +
		`"stats":{"models":{"gemini-2.5-pro":{"totalTokens":555}}}}`
	path := writeLog(t, t.TempDir(), "chats-b.json", doc)
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.SessionID != "whole" || e.Model != "gemini-2.5-flash" || e.InputTokens != 300 {
		t.Errorf("bad entry: %+v", e)
	}
	if want := time.Date(2025, 7, 1, 9, 5, 0, 0, time.UTC); !e.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want per-message %v", e.Timestamp, want)
	}
}

func TestParseMalformedAndUnrelatedLinesSkipped(t *testing.T) {
	content := "{broken\n" +
		`{"tokens":{"input":10,"output":5},"timestamp":"2025-07-01T10:00:00Z"}` + "\n" +
		`{"unrelated":true}` + "\n"
	path := writeLog(t, t.TempDir(), "chat-c.jsonl", content)
	if got := parseFile(path); len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
}

func TestTotalOnlyBackfillsOutput(t *testing.T) {
	line := `{"tokens":{"total":900},"timestamp":"2025-07-01T10:00:00Z","sessionId":"s"}`
	path := writeLog(t, t.TempDir(), "chat-d.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].OutputTokens != 900 {
		t.Fatalf("want output backfilled to 900, got %+v", entries)
	}
}

// Upstream parse_stats_events: a stats block with per-model rows fans out to
// one entry per model, with cached tokens carved back out of input.
func TestParseStatsModelsFanOut(t *testing.T) {
	line := `{"timestamp":"2025-07-01T10:00:00Z","sessionId":"fan",` +
		`"stats":{"models":{` +
		`"gemini-2.5-pro":{"tokens":{"input":1000,"output":200,"cached":400,"thoughts":50}},` +
		`"gemini-2.5-flash":{"tokens":{"input":500,"output":100}}}}}`
	path := writeLog(t, t.TempDir(), "chat-fan.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries (one per model), got %d: %+v", len(entries), entries)
	}
	// Deterministic order: sorted by model name.
	if entries[0].Model != "gemini-2.5-flash" || entries[1].Model != "gemini-2.5-pro" {
		t.Fatalf("models = %q, %q", entries[0].Model, entries[1].Model)
	}
	flash, pro := entries[0], entries[1]
	if flash.InputTokens != 500 || flash.OutputTokens != 100 || flash.CacheReadTokens != 0 {
		t.Errorf("flash row wrong: %+v", flash)
	}
	// Pro row: cached 400 splits off input 1000 -> 600/400; thoughts fold into output.
	if pro.InputTokens != 600 || pro.CacheReadTokens != 400 || pro.OutputTokens != 250 {
		t.Errorf("pro row wrong: in=%d read=%d out=%d", pro.InputTokens, pro.CacheReadTokens, pro.OutputTokens)
	}
}

func TestDiscoverAndMissingDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GEMINI_DATA_DIR", root)
	writeLog(t, filepath.Join(root, "chats"), "a.json", "{}")
	writeLog(t, root, "b.jsonl", "{}")
	if err := os.WriteFile(filepath.Join(root, "ignore.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	files := discoverLogFiles()
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %v", files)
	}
	os.RemoveAll(root)
	if files := discoverLogFiles(); len(files) != 0 {
		t.Fatalf("missing dir must yield empty discovery, got %v", files)
	}
}
