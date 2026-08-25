package augment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xusage/internal/sources"
)

func writeSession(t *testing.T, dir, name, content string) string {
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

const happySession = `{"model":"claude-sonnet-4-20250514","session_id":"sess-a","timestamp":"2025-07-01T10:00:00Z",` +
	`"usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":400,"cache_creation_input_tokens":50},"costUSD":0.03}`

func TestParseHappyPath(t *testing.T) {
	path := writeSession(t, t.TempDir(), "sess-a.json", happySession)
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "claude-sonnet-4-20250514" || e.SessionID != "sess-a" {
		t.Errorf("identity wrong: %+v", e)
	}
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

func TestParseFlatKeySpellings(t *testing.T) {
	doc := `{"model":"o1","session_id":"flat","input_tokens":8,"output_tokens":3,"cached_tokens":2}`
	path := writeSession(t, t.TempDir(), "sess-b.json", doc)
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

func TestParseFileStemFallsBackToSessionID(t *testing.T) {
	// No explicit session id: the file stem becomes the dedup id and session.
	doc := `{"model":"gpt-5","timestamp":"2025-07-01T10:00:00Z","usage":{"input":4,"output":1}}`
	path := writeSession(t, t.TempDir(), "session-xyz.json", doc)
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].ID != "session-xyz" || entries[0].SessionID != "session-xyz" {
		t.Errorf("id/session wrong: %+v", entries[0])
	}
}

func TestParseZeroUsageDropped(t *testing.T) {
	doc := `{"model":"gpt-5","usage":{"input_tokens":0,"output_tokens":0}}`
	path := writeSession(t, t.TempDir(), "sess-c.json", doc)
	if entries, err := parseFile(path); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v, want none", entries, err)
	}
}

func TestParseUnrelatedJsonSkipped(t *testing.T) {
	doc := `{"foo":"bar","baz":123}`
	path := writeSession(t, t.TempDir(), "sess-d.json", doc)
	if entries, err := parseFile(path); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v, want none", entries, err)
	}
}

func TestParseMalformedNotError(t *testing.T) {
	path := writeSession(t, t.TempDir(), "sess-e.json", "{not json")
	if entries, err := parseFile(path); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v, want none, nil", entries, err)
	}
}

func TestDiscoverEnvOverrideWins(t *testing.T) {
	envDir := filepath.Join(t.TempDir(), "envroot")
	homeLike := filepath.Join(t.TempDir(), "homelike", ".augment", "sessions")
	for _, dir := range []string{envDir, homeLike} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSession(t, envDir, "a.json", happySession)
	writeSession(t, homeLike, "b.json", happySession)

	t.Run("env set: only env roots scanned", func(t *testing.T) {
		t.Setenv(dataDirEnv, envDir)
		files, err := discover()
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("got %v, want 1 file under %s", files, envDir)
		}
		if !strings.HasSuffix(files[0], "a.json") {
			t.Errorf("unexpected file: %s", files[0])
		}
	})
	t.Run("multiple env roots comma separated", func(t *testing.T) {
		other := t.TempDir()
		writeSession(t, other, "c.json", "{}")
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
		if sources.All()[i].Name == "augment" {
			found = &sources.All()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("augment source not registered via init()")
	}
	if found.Discover == nil || found.Parse == nil {
		t.Error("registered source has nil Discover/Parse")
	}
}
