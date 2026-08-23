package goose

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/model"
)

// writeFile materializes a fixture file under dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustParse(t *testing.T, path string) []model.Entry {
	t.Helper()
	ents, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(%q) returned error: %v", path, err)
	}
	return ents
}

func TestDiscoverMissingDirReturnsNilNil(t *testing.T) {
	t.Setenv(pathRootEnv, filepath.Join(t.TempDir(), "does-not-exist"))
	paths, err := Discover()
	if err != nil {
		t.Fatalf("Discover() = error %v, want nil", err)
	}
	if paths != nil {
		t.Fatalf("Discover() = %v, want nil for missing dir", paths)
	}
}

func TestDiscoverPathRootOverrideWins(t *testing.T) {
	root := t.TempDir()
	want := writeFile(t, root,
		filepath.Join("data", "sessions", "20260801_abc.jsonl"),
		fixtureLine("s1", `"model_config_json":{"model_name":"gpt-5.2"}`,
			`"created_at":"2026-08-01T10:00:00Z"`, `"accumulated_total_tokens":30`,
			`"accumulated_input_tokens":20`, `"accumulated_output_tokens":10`),
	)

	// An unrelated sessions.db elsewhere must NOT be reported once the
	// override points at root: env wins over every default candidate.
	outside := writeFile(t, t.TempDir(), "sessions.db", "")

	t.Setenv(pathRootEnv, root)
	got, err := Discover()
	if err != nil {
		t.Fatalf("Discover() = error %v, want nil", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Discover() = %v, want [%s] (and never %s)", got, want, outside)
	}
}

func TestDiscoverDefaultCandidatesAndDedup(t *testing.T) {
	home := t.TempDir()
	db := writeFile(t, home,
		filepath.Join(".local", "share", "goose", "sessions", "sessions.db"), "")
	writeFile(t, home,
		filepath.Join(".local", "share", "goose", "sessions", "export.jsonl"),
		fixtureLine("s1", `"model_config_json":{"model_name":"gpt-5.2"}`,
			`"created_at":"2026-08-01T10:00:00Z"`, `"total_tokens":10`),
	)

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows resolution of os.UserHomeDir
	paths, err := Discover()
	if err != nil {
		t.Fatalf("Discover() = error %v, want nil", err)
	}
	want := filepath.Join(home, ".local", "share", "goose", "sessions")
	if len(paths) != 2 || paths[0] != filepath.Join(want, "export.jsonl") ||
		paths[1] != db {
		t.Fatalf("Discover() = %v, want export.jsonl then sessions.db under %s",
			paths, want)
	}
}

func TestParseJSONLHappyPath(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "sessions.jsonl", `
{"id":"acc","provider_name":"anthropic","model_config_json":"{\"model_name\":\"claude-sonnet-4-20250514\"}","created_at":"2026-08-01 01:02:03","total_tokens":180,"input_tokens":100,"output_tokens":50,"accumulated_total_tokens":180,"accumulated_input_tokens":100,"accumulated_output_tokens":50}
{"id":"millis","provider_name":"openai","model_config_json":{"model_name":"gpt-5.2"},"created_at":1754035200000,"input_tokens":7,"output_tokens":3,"total_tokens":25}
{"id":"rfc","model_config_json":"{\"model_name\":\"gemini-3-pro\"}","created_at":"2026-08-02T05:06:07Z","input_tokens":11,"output_tokens":4,"total_tokens":15}
{"id":"bare","model_config_json":{"model_name":"qwen3-max"},"created_at":"2026-08-03","input_tokens":9,"output_tokens":1,"total_tokens":10}

{not json at all}
{"id":"","model_config_json":{"model_name":"x"},"created_at":"2026-08-04T00:00:00Z","total_tokens":5}
{"id":"nomodel","created_at":"2026-08-04T00:00:00Z","total_tokens":5}
{"id":"zerotok","model_config_json":{"model_name":"x"},"created_at":"2026-08-04T00:00:00Z"}
`[1:])

	ents := mustParse(t, p)
	if len(ents) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(ents), ents)
	}
	type expect struct {
		id             string
		model          string
		in, out, extra uint64
	}
	wants := []expect{
		{"acc", "claude-sonnet-4-20250514", 100, 50, 30},
		{"millis", "gpt-5.2", 7, 3, 15},
		{"rfc", "gemini-3-pro", 11, 4, 0},
		{"bare", "qwen3-max", 9, 1, 0},
	}
	for i, w := range wants {
		e := ents[i]
		if e.ID != w.id || e.Model != w.model {
			t.Errorf("entry %d = {%s %s}, want {%s %s}", i, e.ID, e.Model, w.id, w.model)
			continue
		}
		if e.InputTokens != w.in || e.OutputTokens != w.out || e.ExtraTotalTokens != w.extra {
			t.Errorf("entry %s tokens = (%d,%d,%d), want (%d,%d,%d)",
				w.id, e.InputTokens, e.OutputTokens, e.ExtraTotalTokens, w.in, w.out, w.extra)
		}
		// Fields the loader's dedup keys on must always be present.
		if e.SessionID != w.id || e.RequestID != w.id {
			t.Errorf("entry %s dedup identity = session %q request %q, want both %q",
				w.id, e.SessionID, e.RequestID, w.id)
		}
		if e.Timestamp.IsZero() || e.Timestamp.Location() != time.UTC {
			t.Errorf("entry %s timestamp %+v not UTC or zero", w.id, e.Timestamp)
		}
		if e.Messages != 1 {
			t.Errorf("entry %s messages = %d, want 1", w.id, e.Messages)
		}
	}
	// The first row uses SQLite datetime text.
	if got, want := ents[0].Timestamp.Format(time.RFC3339), "2026-08-01T01:02:03Z"; got != want {
		t.Errorf("sqlite datetime parsed as %s, want %s", got, want)
	}
}

func TestParseJSONArrayAndSingleObject(t *testing.T) {
	dir := t.TempDir()
	arr := writeFile(t, dir, "all.json", `[
 {"id":"a","model_config_json":{"model_name":"m1"},"created_at":"2026-08-01T00:00:00Z","total_tokens":5},
 {"id":"b","model_config_json":{"model_name":"m2"},"created_at":"2026-08-02T00:00:00Z","total_tokens":6}
]`)
	if ents := mustParse(t, arr); len(ents) != 2 {
		t.Fatalf("array export: got %d entries, want 2", len(ents))
	}
	one := writeFile(t, dir, "one.json",
		`{"id":"only","model_config_json":{"model_name":"m"},
		  "created_at":"2026-08-01T00:00:00+02:00","total_tokens":5}`)
	ents := mustParse(t, one)
	if len(ents) != 1 {
		t.Fatalf("single-object export: got %d entries, want 1", len(ents))
	}
	// Offsets normalize to UTC instant, not wall clock.
	if got := ents[0].Timestamp.Format(time.RFC3339); got != "2026-07-31T22:00:00Z" {
		t.Errorf("offset-aware timestamp = %s, want 2026-07-31T22:00:00Z", got)
	}
}

func TestParseGarbageJSONIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "other.json", `{"hello":"world"}`)
	ents := mustParse(t, p)
	if len(ents) != 0 {
		t.Fatalf("got %d entries from unrelated JSON, want 0", len(ents))
	}
}

func TestParseUnreadableFileErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "missing.jsonl")
	if _, err := Parse(p); err == nil {
		t.Fatal("Parse on unreadable file should return an error")
	}
}

// fixtureLine builds one goose session-export JSONL object with the given
// raw member snippets.
func fixtureLine(id string, members ...string) string {
	line := `{"id":"` + id + `"`
	for _, m := range members {
		line += "," + m
	}
	return line + "}\n"
}
