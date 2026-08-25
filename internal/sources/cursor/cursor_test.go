package cursor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/model"
)

func writeExport(t *testing.T, dir, name, content string) string {
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
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","model":"gpt-5.2","input_tokens":1000,` +
		`"output_tokens":200,"cache_creation_input_tokens":100,"cache_read_input_tokens":50,` +
		`"total_tokens":1300,"cost":0.05,"request_id":"req-1","session_id":"sess-1","project":"myproj"}`
	p := writeExport(t, dir, "usage.jsonl", line+"\n")
	entries := mustParseFile(t, p)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "gpt-5.2" || e.SessionID != "sess-1" || e.Project != "myproj" {
		t.Errorf("identity wrong: %+v", e)
	}
	// cached (50) carved out of input (1000) -> input 950, cache read 50.
	if e.InputTokens != 950 || e.CacheReadTokens != 50 {
		t.Errorf("input/cache wrong: input=%d cache=%d", e.InputTokens, e.CacheReadTokens)
	}
	if e.OutputTokens != 200 || e.CacheCreationTokens != 100 {
		t.Errorf("tokens wrong: out=%d cc=%d", e.OutputTokens, e.CacheCreationTokens)
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.05 {
		t.Errorf("cost wrong: %v", e.ReportedCost)
	}
	want := time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC)
	if !e.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", e.Timestamp, want)
	}
}

func TestParseAlternateSpellings(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","model_name":"gpt-5.2","prompt_tokens":500,` +
		`"completion_tokens":80,"cached_tokens":30}`
	p := writeExport(t, dir, "u.jsonl", line+"\n")
	entries := mustParseFile(t, p)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.InputTokens != 470 || e.CacheReadTokens != 30 || e.OutputTokens != 80 {
		t.Errorf("spelling normalize wrong: in=%d cache=%d out=%d", e.InputTokens, e.CacheReadTokens, e.OutputTokens)
	}
}

func TestParseTotalOnlyBecomesExtra(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","model":"gpt-5.2","total_tokens":777}`
	p := writeExport(t, dir, "u.jsonl", line+"\n")
	entries := mustParseFile(t, p)
	if len(entries) != 1 || entries[0].ExtraTotalTokens != 777 {
		t.Fatalf("want ExtraTotalTokens=777, got %+v", entries)
	}
}

func TestParseMalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	content := "not json\n{broken\n" +
		`{"timestamp":"2026-03-29T07:00:00.000Z","model":"gpt-5.2","input_tokens":10,"output_tokens":5}` + "\n"
	p := writeExport(t, dir, "u.jsonl", content)
	entries := mustParseFile(t, p)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
}

func TestParseZeroTokensSkipped(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","model":"gpt-5.2"}`
	p := writeExport(t, dir, "u.jsonl", line+"\n")
	if entries := mustParseFile(t, p); len(entries) != 0 {
		t.Fatalf("want 0 entries, got %d", len(entries))
	}
}

func TestDiscoverEnvOverride(t *testing.T) {
	dir := t.TempDir()
	writeExport(t, dir, "usage.jsonl",
		`{"timestamp":"2026-03-29T07:00:00.000Z","model":"gpt-5.2","input_tokens":1}`+"\n")
	t.Setenv("CURSOR_HOME", dir)
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d: %v", len(files), files)
	}
}

func TestDiscoverMissingDirReturnsNil(t *testing.T) {
	t.Setenv("CURSOR_HOME", filepath.Join(t.TempDir(), "missing"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover error = %v, want nil", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}
