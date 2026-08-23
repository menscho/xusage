package pi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// lookupRegistered finds this package's registered source in the global
// registry (the registration happens in init before any test runs).
func lookupRegistered(name string) (sources.Source, bool) {
	for _, s := range sources.All() {
		if s.Name == name {
			return s, true
		}
	}
	return sources.Source{}, false
}

// writeSession materializes a session file under dir/sessions/<project>/ and
// returns its path.
func writeSession(t *testing.T, dir, project, name, content string) string {
	t.Helper()
	d := filepath.Join(dir, "sessions", project)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustParse(t *testing.T, path string) []model.Entry {
	t.Helper()
	entries, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse(%q) returned error: %v", path, err)
	}
	return entries
}

// usageLine wraps a usage payload in a valid assistant record.
func usageLine(usage string) string {
	return `{"type":"message","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","model":"gpt-5","usage":{` + usage + `}}}`
}

func TestParseHappyPath(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		check func(t *testing.T, e model.Entry)
	}{
		{
			name: "full split usage",
			line: usageLine(`"input":100,"output":200,"cacheRead":30,"cacheWrite":40,"totalTokens":370,"cost":{"total":0.0125}`),
			check: func(t *testing.T, e model.Entry) {
				if e.InputTokens != 100 || e.OutputTokens != 200 ||
					e.CacheReadTokens != 30 || e.CacheCreationTokens != 40 {
					t.Fatalf("token split wrong: %+v", e)
				}
				if e.ExtraTotalTokens != 0 {
					t.Errorf("ExtraTotalTokens = %d, want 0", e.ExtraTotalTokens)
				}
				if e.ReportedCost == nil || *e.ReportedCost != 0.0125 {
					t.Errorf("ReportedCost = %v, want 0.0125", e.ReportedCost)
				}
				if e.Model != "gpt-5" || e.RawModel != "gpt-5" {
					t.Errorf("model = %q/%q, want gpt-5/gpt-5", e.Model, e.RawModel)
				}
			},
		},
		{
			name: "lone total fills output tokens",
			line: usageLine(`"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":333`),
			check: func(t *testing.T, e model.Entry) {
				if e.OutputTokens != 333 {
					t.Errorf("OutputTokens = %d, want 333 (total fallback)", e.OutputTokens)
				}
				if e.ExtraTotalTokens != 0 {
					t.Errorf("ExtraTotalTokens = %d, want 0", e.ExtraTotalTokens)
				}
			},
		},
		{
			name: "overflow beyond known buckets goes to extra total",
			line: usageLine(`"input":100,"output":50,"cacheRead":25,"cacheWrite":0,"totalTokens":200`),
			check: func(t *testing.T, e model.Entry) {
				if e.OutputTokens != 50 {
					t.Errorf("OutputTokens = %d, want 50 (kept)", e.OutputTokens)
				}
				if e.ExtraTotalTokens != 25 {
					t.Errorf("ExtraTotalTokens = %d, want 25", e.ExtraTotalTokens)
				}
			},
		},
		{
			name: "object cost carries reported cost",
			line: usageLine(`"input":10,"output":20,"cost":{"total":0.5}`),
			check: func(t *testing.T, e model.Entry) {
				if e.ReportedCost == nil || *e.ReportedCost != 0.5 {
					t.Errorf("ReportedCost = %v, want 0.5", e.ReportedCost)
				}
			},
		},
		{
			name: "bare number cost carries no display cost",
			line: usageLine(`"input":10,"output":20,"cost":0.5`),
			check: func(t *testing.T, e model.Entry) {
				if e.ReportedCost != nil {
					t.Errorf("ReportedCost = %v, want nil (cost must be an object)", e.ReportedCost)
				}
				if e.InputTokens != 10 || e.OutputTokens != 20 {
					t.Errorf("tokens lost when cost is a bare number: %+v", e)
				}
			},
		},
		{
			name: "bad-typed sibling fields coerce to zero and keep the rest",
			line: usageLine(`"input":100,"output":"x","cacheRead":-1,"cacheWrite":true`),
			check: func(t *testing.T, e model.Entry) {
				if e.InputTokens != 100 {
					t.Errorf("InputTokens = %d, want 100", e.InputTokens)
				}
				if e.OutputTokens != 0 || e.CacheReadTokens != 0 || e.CacheCreationTokens != 0 {
					t.Errorf("non-integer tokens must coerce to 0: %+v", e)
				}
			},
		},
		{
			name: "absent type field is accepted",
			line: `{"timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","model":"m","usage":{"input":7,"output":7}}}`,
			check: func(t *testing.T, e model.Entry) {
				if e.TotalTokens() != 14 {
					t.Errorf("TotalTokens = %d, want 14", e.TotalTokens())
				}
			},
		},
		{
			name: "timestamp with explicit offset converts to UTC",
			line: `{"type":"message","timestamp":"2026-01-02T05:30:00+05:30","message":{"role":"assistant","model":"gpt-5","usage":{"input":1,"output":2}}}`,
			check: func(t *testing.T, e model.Entry) {
				want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
				if !e.Timestamp.Equal(want) {
					t.Errorf("Timestamp = %v, want %v", e.Timestamp, want)
				}
			},
		},
		{
			name: "missing model still counts",
			line: `{"type":"message","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","usage":{"input":1,"output":2}}}`,
			check: func(t *testing.T, e model.Entry) {
				if e.Model != "" || e.RawModel != "" {
					t.Errorf("model = %q/%q, want empty", e.Model, e.RawModel)
				}
				if e.TotalTokens() != 3 {
					t.Errorf("TotalTokens = %d, want 3", e.TotalTokens())
				}
			},
		},
		{
			name: "non-string type field is tolerated",
			line: `{"type":7,"timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","model":"gpt-5","usage":{"input":1,"output":2}}}`,
			check: func(t *testing.T, e model.Entry) {
				if e.TotalTokens() != 3 {
					t.Errorf("TotalTokens = %d, want 3", e.TotalTokens())
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeSession(t, t.TempDir(), "proj-a", "agent_s1.jsonl", tt.line+"\n")
			entries := mustParse(t, path)
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			e := entries[0]
			if tt.name != "timestamp with explicit offset converts to UTC" {
				want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
				if !e.Timestamp.Equal(want) {
					t.Errorf("Timestamp = %v, want exactly %v (UTC)", e.Timestamp, want)
				}
			}
			tt.check(t, e)
		})
	}
}

func TestParseSkipsNonUsageRecords(t *testing.T) {
	lines := []string{
		`not json at all`,
		`{"type":"summary","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","usage":{"input":1}}}`,
		`{"type":"message","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"user","usage":{"input":9,"output":9}}}`,
		`{"type":"message","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant"}}`,
		`{"type":"message","timestamp":"not-a-time","message":{"role":"assistant","model":"m","usage":{"input":1,"output":2}}}`,
		`{"type":"message","timestamp":"","message":{"role":"assistant","model":"m","usage":{"input":1,"output":2}}}`,
		`{"type":"message","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","model":"m","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0}}}`,
		`{"type":"message","timestamp":"2026-01-02T00:00:00.000Z","message":{"role":"assistant","model":"m","usage":{"input":12.5,"output":"7"}}}`,
		``,
	}
	var buf string
	for _, l := range lines {
		buf += l + "\n"
	}
	path := writeSession(t, t.TempDir(), "proj", "agent_s.jsonl", buf)
	entries := mustParse(t, path)
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0; entries: %+v", len(entries), entries)
	}
}

func TestParseProjectAndSessionFromPathLayout(t *testing.T) {
	tests := []struct {
		name        string
		file        string
		wantProject string
		wantSession string
	}{
		{
			name:        "agent-prefixed session file",
			file:        filepath.Join("agent_abc123.jsonl"),
			wantProject: "proj-x",
			wantSession: "abc123",
		},
		{
			name:        "plain stem without underscore",
			file:        filepath.Join("session.jsonl"),
			wantProject: "proj-x",
			wantSession: "session",
		},
		{
			name:        "nested subdirectory",
			file:        filepath.Join("sub", "dir", "agent_nested.jsonl"),
			wantProject: "proj-x",
			wantSession: "nested",
		},
	}
	line := usageLine(`"input":1,"output":2`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			full := filepath.Join(dir, "sessions", tt.wantProject, tt.file)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			entries := mustParse(t, full)
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			if entries[0].Project != tt.wantProject {
				t.Errorf("Project = %q, want %q", entries[0].Project, tt.wantProject)
			}
			if entries[0].SessionID != tt.wantSession {
				t.Errorf("SessionID = %q, want %q", entries[0].SessionID, tt.wantSession)
			}
		})
	}
}

func TestExtractProjectWithoutSessionsSegment(t *testing.T) {
	got := extractProject(filepath.Join("some", "other", "place", "f.jsonl"))
	if got != "unknown" {
		t.Errorf("extractProject = %q, want %q", got, "unknown")
	}
}

func TestDiscoverMissingDirReturnsNilNoError(t *testing.T) {
	t.Setenv(piAgentDirEnv, "")
	home, _ := os.UserHomeDir()
	missing := filepath.Join(home, ".pi-agent-missing-does-not-exist")
	t.Setenv("USERPROFILE", missing)
	t.Setenv("HOME", missing)

	files, err := Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}
	if files != nil {
		t.Errorf("Discover() = %v, want nil", files)
	}
}

func TestDiscoverEnvOverrideWinsOverHomeDefault(t *testing.T) {
	tmp := t.TempDir()
	custom := filepath.Join(tmp, "custom-sessions")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(custom, "agent_env.jsonl")
	if err := os.WriteFile(f, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(piAgentDirEnv, custom)

	files, err := Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(files) != 1 || files[0] != f {
		t.Errorf("Discover() = %v, want [%s]", files, f)
	}
}

func TestDiscoverEnvListDedupsAndSkipsMissing(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(piAgentDirEnv, " "+a+" , "+b+","+a+","+filepath.Join(tmp, "nope")+" ")

	files, err := Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(files) != 0 {
		t.Errorf("Discover() = %v, want no jsonl files", files)
	}
}

func TestDiscoverRecursiveJSONL(t *testing.T) {
	tmp := t.TempDir()
	sessions := filepath.Join(tmp, "sessions", "proj")
	nested := filepath.Join(sessions, "deep", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	wanted := filepath.Join(nested, "agent_x.jsonl")
	if err := os.WriteFile(wanted, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "ignore.txt"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(piAgentDirEnv, sessions)

	files, err := Discover()
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(files) != 1 || files[0] != wanted {
		t.Errorf("Discover() = %v, want [%s]", files, wanted)
	}
}

func TestRegisteredSourceShape(t *testing.T) {
	src, ok := lookupRegistered("pi")
	if !ok {
		t.Fatal("pi source not registered")
	}
	if src.Name != "pi" {
		t.Errorf("Name = %q, want pi", src.Name)
	}
	if src.Discover == nil || src.Parse == nil {
		t.Fatal("Discover and Parse must be non-nil")
	}
}

func TestEntryFieldsForDedupRelevance(t *testing.T) {
	// The loader dedupes on Provider+ID+RequestID; pi carries none of those,
	// so every parsed entry must keep an empty ID and be kept as-is.
	path := writeSession(t, t.TempDir(), "p", "agent_d.jsonl", usageLine(`"input":5,"output":6`)+"\n")
	e := mustParse(t, path)[0]
	if e.ID != "" || e.RequestID != "" {
		t.Errorf("ID/RequestID = %q/%q, want empty so dedup keeps all pi entries", e.ID, e.RequestID)
	}
	if e.Messages != 1 {
		t.Errorf("Messages = %d, want 1", e.Messages)
	}
	if e.Provider != "" {
		t.Errorf("Provider = %q, want empty (engine sets it)", e.Provider)
	}
	if e.IsSidechain || e.IsAPIError {
		t.Error("IsSidechain/IsAPIError must be false")
	}
}
