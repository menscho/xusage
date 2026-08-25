package roocode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xusage/internal/sources"
)

// writeJSONL materializes an inline JSONL fixture in a temp dir and returns
// its path.
func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

const tsValid = 1767312000000 // 2026-01-02T00:00:00Z, matches upstream tests

func TestParseHappyPath(t *testing.T) {
	path := writeJSONL(t,
		`{"id":"msg-1","role":"assistant","providerID":"anthropic","modelID":"claude-sonnet-4-20250514","session_id":"session-a","time":{"created":1767312000000},"tokens":{"input":100,"output":50,"reasoning":5,"cache":{"read":10,"write":20}},"cost":0.02}`,
	)
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.ID != "msg-1" || e.SessionID != "session-a" || e.Model != "claude-sonnet-4-20250514" {
		t.Errorf("identity fields wrong: %+v", e)
	}
	if e.InputTokens != 100 || e.OutputTokens != 50 || e.CacheReadTokens != 10 ||
		e.CacheCreationTokens != 20 || e.ExtraTotalTokens != 5 {
		t.Errorf("token mapping wrong: %+v", e)
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.02 {
		t.Errorf("ReportedCost = %v, want 0.02", e.ReportedCost)
	}
	want := time.UnixMilli(tsValid).UTC()
	if !e.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, want)
	}
	if e.Messages != 1 {
		t.Errorf("Messages = %d, want 1", e.Messages)
	}
	if e.Provider != "" {
		t.Errorf("Provider = %q, want empty (engine sets it)", e.Provider)
	}
}

func TestParseTotalFallback(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		out   uint64
		extra uint64
	}{
		{
			name:  "total backfills zero output",
			line:  `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":100,"output":0,"reasoning":25,"total":175}}`,
			out:   50,
			extra: 25,
		},
		{
			name:  "gap spills to extra",
			line:  `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":100,"output":50,"total":200}}`,
			out:   50,
			extra: 50,
		},
		{
			name:  "lone total becomes output",
			line:  `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"total":234}}`,
			out:   234,
			extra: 0,
		},
		{
			name:  "reasoning without total",
			line:  `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":10,"output":5,"reasoning":3}}`,
			out:   5,
			extra: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := parseFile(writeJSONL(t, tt.line))
			if err != nil {
				t.Fatalf("parseFile: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			if entries[0].OutputTokens != tt.out || entries[0].ExtraTotalTokens != tt.extra {
				t.Errorf("output=%d extra=%d, want out=%d extra=%d",
					entries[0].OutputTokens, entries[0].ExtraTotalTokens, tt.out, tt.extra)
			}
		})
	}
}

func TestParseRejections(t *testing.T) {
	valid := `{"id":"keep","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":1,"output":1}}`
	tests := []struct {
		name string
		line string
	}{
		{"user role skipped", `{"id":"u","role":"user","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":9,"output":9}}`},
		{"missing role", `{"id":"n","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":9,"output":9}}`},
		{"empty model id", `{"id":"m","role":"assistant","modelID":"","time":{"created":1767312000000},"tokens":{"input":9,"output":9}}`},
		{"null model id", `{"id":"m","role":"assistant","modelID":null,"time":{"created":1767312000000},"tokens":{"input":9,"output":9}}`},
		{"missing tokens", `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000}}`},
		{"null tokens", `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":null}`},
		{"non-object tokens", `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":0}`},
		{"zero usage dropped", `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":0,"output":0}}`},
		{"missing timestamp", `{"id":"m","role":"assistant","modelID":"gpt-5","tokens":{"input":9,"output":9}}`},
		{"non-positive timestamp", `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":0},"tokens":{"input":9,"output":9}}`},
		{"malformed json", `{"id":"m","role":"assistant",`},
		{"array line", `[1,2,3]`},
		{"plain text", `hello world`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := parseFile(writeJSONL(t, tt.line, valid))
			if err != nil {
				t.Fatalf("parseFile: %v", err)
			}
			if len(entries) != 1 || entries[0].ID != "keep" {
				t.Errorf("got %d entries (%v), want exactly the valid one", len(entries), entries)
			}
		})
	}
}

func TestParseLenientFields(t *testing.T) {
	// Non-object cache and seconds-epoch time both survive; empty session_id
	// falls back to empty rather than failing.
	path := writeJSONL(t,
		`{"id":"msg-1","role":"assistant","providerID":"openai","modelID":"gpt-5","time":{"created":1767312000},"tokens":{"input":100,"output":10,"cache":0}}`,
	)
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.CacheReadTokens != 0 || e.CacheCreationTokens != 0 {
		t.Errorf("non-object cache should read as zero: %+v", e)
	}
	if want := time.UnixMilli(1767312000 * 1000).UTC(); !e.Timestamp.Equal(want) {
		t.Errorf("seconds epoch not normalized: got %v want %v", e.Timestamp, want)
	}
}

// A non-numeric cost is absent, never a reason to drop the record.
func TestParseNonNumericCostKeptAsAbsent(t *testing.T) {
	line := `{"id":"m","role":"assistant","modelID":"gpt-5","time":{"created":1767312000000},"tokens":{"input":9,"output":9},"cost":"expensive"}`
	entries, err := parseFile(writeJSONL(t, line))
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want the record kept: %v", len(entries), entries)
	}
	if entries[0].ReportedCost != nil {
		t.Errorf("ReportedCost = %v, want nil for a non-numeric cost", *entries[0].ReportedCost)
	}
}

func TestParseDedupRelevantFieldsPresent(t *testing.T) {
	// Same message id across two files: loader dedup keys on Provider+ID, so
	// the ID must survive parsing intact on every entry.
	line := `{"id":"dup-1","role":"assistant","modelID":"gpt-5","session_id":"s","time":{"created":1767312000000},"tokens":{"input":5,"output":5}}`
	dir := t.TempDir()
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		entries, err := parseFile(filepath.Join(dir, name))
		if err != nil || len(entries) != 1 || entries[0].ID != "dup-1" {
			t.Fatalf("%s: entries=%v err=%v", name, entries, err)
		}
	}
}

func TestParseEmptyAndMissingFiles(t *testing.T) {
	t.Run("empty file yields no entries", func(t *testing.T) {
		entries, err := parseFile(writeJSONL(t))
		if err != nil || len(entries) != 0 {
			t.Errorf("entries=%v err=%v, want none", entries, err)
		}
	})
	t.Run("missing file errors", func(t *testing.T) {
		if _, err := parseFile(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
			t.Error("unreadable file should return an error")
		}
	})
}

func TestDiscoverMissingDirReturnsNilNil(t *testing.T) {
	t.Setenv(EnvDataDir, filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := discover()
	if files != nil || err != nil {
		t.Errorf("discover() = %v, %v; want nil, nil", files, err)
	}
}

func TestDiscoverEnvOverrideWins(t *testing.T) {
	envDir := filepath.Join(t.TempDir(), "envroot")
	homeLike := t.TempDir()
	for _, dir := range []string{envDir, homeLike} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(envDir, "a.jsonl")
	write(envDir, filepath.Join("nested", "b.jsonl"))
	write(homeLike, "c.jsonl")

	t.Run("env set: only env roots scanned", func(t *testing.T) {
		t.Setenv(EnvDataDir, envDir)
		files, err := discover()
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("got %v, want 2 files under %s", files, envDir)
		}
		if !strings.HasSuffix(files[0], "a.jsonl") || !strings.HasSuffix(files[1], "b.jsonl") {
			t.Errorf("unexpected file list: %v", files)
		}
	})
	t.Run("multiple env roots comma separated", func(t *testing.T) {
		other := t.TempDir()
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		write(other, "d.jsonl")
		t.Setenv(EnvDataDir, envDir+","+other)
		files, err := discover()
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		if len(files) != 3 {
			t.Fatalf("got %v, want 3 files across roots", files)
		}
	})
	t.Run("blank env entries ignored", func(t *testing.T) {
		t.Setenv(EnvDataDir, " , "+envDir+" , ")
		files, err := discover()
		if err != nil || len(files) != 2 {
			t.Errorf("discover() = %v, %v; want 2 files", files, err)
		}
	})
}

func TestRegistration(t *testing.T) {
	var found *sources.Source
	for i, s := range sources.All() {
		if s.Name == "roocode" {
			found = &sources.All()[i]
			break
		}
	}
	if found == nil {
		t.Fatal("roocode source not registered via init()")
	}
	if found.Discover == nil || found.Parse == nil {
		t.Error("registered source has nil Discover/Parse")
	}
}
