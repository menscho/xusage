package zed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// writeDoc materializes an inline fixture in a temp dir and returns its path.
func writeDoc(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const happyTurn = `{"model":"claude-sonnet-4-20250514","session_id":"thread-a","timestamp":"2025-07-01T10:00:00Z",` +
	`"usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":400,"cache_creation_input_tokens":50},"costUSD":0.03}`

func TestParseHappyPath(t *testing.T) {
	path := writeDoc(t, t.TempDir(), "thread-a.jsonl", happyTurn+"\n")
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "claude-sonnet-4-20250514" || e.SessionID != "thread-a" {
		t.Errorf("identity wrong: %+v", e)
	}
	// Cache read (400) splits off input 1000 -> 600 input / 400 cache read.
	if e.InputTokens != 600 || e.CacheReadTokens != 400 || e.CacheCreationTokens != 50 || e.OutputTokens != 200 {
		t.Errorf("tokens wrong: in=%d read=%d cc=%d out=%d", e.InputTokens, e.CacheReadTokens, e.CacheCreationTokens, e.OutputTokens)
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.03 {
		t.Errorf("cost = %v, want 0.03", e.ReportedCost)
	}
	want := time.Date(2025, 7, 1, 10, 0, 0, 0, time.UTC)
	if !e.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", e.Timestamp, want)
	}
}

func TestParseArrayDocument(t *testing.T) {
	doc := `[{"model":"gpt-5","session_id":"s","usage":{"input":10,"output":2}},` +
		`{"model":"gpt-5","session_id":"s","usage":{"input":5,"output":1}}]`
	path := writeDoc(t, t.TempDir(), "thread-b.json", doc)
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].InputTokens != 10 || entries[1].OutputTokens != 1 {
		t.Errorf("tokens wrong: %+v", entries)
	}
}

func TestParseTokensEnvelope(t *testing.T) {
	doc := `{"turns":[{"model":"gemini-2.5-pro","thread_id":"t1","usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}]}`
	path := writeDoc(t, t.TempDir(), "thread-c.json", doc)
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 || entries[0].Model != "gemini-2.5-pro" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseTokensBlockFlatKeys(t *testing.T) {
	// Flat key spellings (no nested usage block) are still consumed.
	line := `{"model":"o1","session_id":"flat","input_tokens":8,"output_tokens":3,"cached_tokens":2}`
	path := writeDoc(t, t.TempDir(), "thread-d.jsonl", line+"\n")
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].InputTokens != 6 || entries[0].CacheReadTokens != 2 || entries[0].OutputTokens != 3 {
		t.Errorf("tokens wrong: %+v", entries[0])
	}
}

func TestParseMalformedAndUnrelatedSkipped(t *testing.T) {
	content := "{broken\n" +
		happyTurn + "\n" +
		`{"no_model":true}` + "\n"
	path := writeDoc(t, t.TempDir(), "thread-e.jsonl", content)
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
}

func TestParseZeroUsageDropped(t *testing.T) {
	line := `{"model":"gpt-5","usage":{"input_tokens":0,"output_tokens":0}}`
	path := writeDoc(t, t.TempDir(), "thread-f.jsonl", line+"\n")
	if entries, err := parseFile(path); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v, want none", entries, err)
	}
}

func TestDiscoverEnvOverrideWins(t *testing.T) {
	envDir := filepath.Join(t.TempDir(), "envroot")
	homeLike := filepath.Join(t.TempDir(), "homelike", ".local", "share", "zed", "threads")
	for _, dir := range []string{envDir, homeLike} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeDoc(t, envDir, "a.jsonl", happyTurn+"\n")
	writeDoc(t, homeLike, "b.jsonl", happyTurn+"\n")

	t.Run("env set: only env roots scanned", func(t *testing.T) {
		t.Setenv(dataDirEnv, envDir)
		files, err := discover()
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("got %v, want 1 file under %s", files, envDir)
		}
		if !strings.HasSuffix(files[0], "a.jsonl") {
			t.Errorf("unexpected file: %s", files[0])
		}
	})
	t.Run("multiple env roots comma separated", func(t *testing.T) {
		other := t.TempDir()
		writeDoc(t, other, "c.json", "{}\n")
		t.Setenv(dataDirEnv, envDir+","+other)
		files, err := discover()
		if err != nil || len(files) != 2 {
			t.Fatalf("discover() = %v, %v; want 2 files", files, err)
		}
	})
}

func TestDiscoverMissingDirReturnsNilNil(t *testing.T) {
	t.Setenv(dataDirEnv, filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := discover()
	if files != nil || err != nil {
		t.Errorf("discover() = %v, %v; want nil, nil", files, err)
	}
}

func TestRegistration(t *testing.T) {
	var found *sources.Source
	for i := range sources.All() {
		if sources.All()[i].Name == "zed" {
			found = &sources.All()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("zed source not registered via init()")
	}
	if found.Discover == nil || found.Parse == nil {
		t.Error("registered source has nil Discover/Parse")
	}
}

var _ = model.Entry{}
