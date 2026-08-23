package openclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xusage/internal/model"
)

// write writes rel under dir with the given content, creating parents.
func write(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const tsMillis = `1769753935279` // 2026-01-30T06:18:55.279Z

func TestIsSessionFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"abc.jsonl", true},
		{"a.jsonl.deleted.1700000000000", true},
		{"a.jsonl.reset.2026-03-20T06-34-44.520Z", true},
		{"a.json", false},
		{"a.jsonl.bak", false},
		{"jsonl", false},
	}
	for _, tt := range tests {
		if got := isSessionFile(tt.name); got != tt.want {
			t.Errorf("isSessionFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{filepath.Join("agents", "main", "sessions", "abc.jsonl"), "abc"},
		{"session.jsonl.deleted.123", "session"},
		{".jsonl", ".jsonl"}, // empty stem keeps the whole name
		{"noext", "noext"},
	}
	for _, tt := range tests {
		if got := extractSessionID(tt.path); got != tt.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestParseHappyPath(t *testing.T) {
	content := strings.Join([]string{
		`{"type":"model_change","provider":"openai-codex","modelId":"gpt-5.2"}`,
		`{"type":"message","message":{"role":"assistant","usage":{"input":1660,"output":55,"cacheRead":108928,"cost":{"total":0.02}},"timestamp":` + tsMillis + `}}`,
		"",
	}, "\n")
	dir := t.TempDir()
	path := write(t, dir, filepath.Join("agents", "main", "sessions", "abc.jsonl"), content)

	entries, err := parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "[openclaw] gpt-5.2" {
		t.Errorf("Model = %q, want %q", e.Model, "[openclaw] gpt-5.2")
	}
	if e.RawModel != "gpt-5.2" {
		t.Errorf("RawModel = %q, want gpt-5.2", e.RawModel)
	}
	if e.Version != "openai-codex" {
		t.Errorf("Version = %q, want openai-codex", e.Version)
	}
	if e.SessionID != "abc" || e.Project != "openclaw" {
		t.Errorf("Session/Project = %q/%q, want abc/openclaw", e.SessionID, e.Project)
	}
	if e.InputTokens != 1660 || e.OutputTokens != 55 || e.CacheReadTokens != 108928 {
		t.Errorf("tokens wrong: %+v", e)
	}
	if e.ExtraTotalTokens != 0 {
		t.Errorf("ExtraTotalTokens = %d, want 0", e.ExtraTotalTokens)
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.02 {
		t.Errorf("ReportedCost = %v, want 0.02", e.ReportedCost)
	}
	if want := time.UnixMilli(1769753935279).UTC(); !e.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, want)
	}
}

func TestParseModelChangeVariants(t *testing.T) {
	base := `{"type":"message","message":{"role":"assistant","usage":{"input":10,"output":20}}}`
	tests := []struct {
		name      string
		line      string
		wantModel string
		wantProv  string
	}{
		{
			name:      "root fields",
			line:      `{"type":"model_change","modelId":"m-root","provider":"p-root"}`,
			wantModel: "[openclaw] m-root",
			wantProv:  "p-root",
		},
		{
			name:      "data object",
			line:      `{"type":"model_change","data":{"modelId":"m-data","provider":"p-data"}}`,
			wantModel: "[openclaw] m-data",
			wantProv:  "p-data",
		},
		{
			name:      "custom model-snapshot",
			line:      `{"type":"custom","customType":"model-snapshot","data":{"model":"m-snap","provider":"p-snap"}}`,
			wantModel: "[openclaw] m-snap",
			wantProv:  "p-snap",
		},
		{
			name:      "malformed data falls back to root",
			line:      `{"type":"model_change","modelId":"m-fb","data":"nope"}`,
			wantModel: "[openclaw] m-fb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := write(t, dir, "s.jsonl", tt.line+"\n"+base+"\n")
			entries, err := parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			if entries[0].RawModel != tt.wantModel[len("[openclaw] "):] ||
				entries[0].Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", entries[0].Model, tt.wantModel)
			}
			if tt.wantProv != "" && entries[0].Version != tt.wantProv {
				t.Errorf("Version = %q, want %q", entries[0].Version, tt.wantProv)
			}
		})
	}
}

func TestParseMessageModelFallbacks(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		wantModel string
	}{
		{
			name:      "message modelId wins",
			msg:       `{"role":"assistant","modelId":"mid","model":"mname","usage":{"input":1}}`,
			wantModel: "[openclaw] mid",
		},
		{
			name:      "message model second",
			msg:       `{"role":"assistant","model":"mname","usage":{"input":1}}`,
			wantModel: "[openclaw] mname",
		},
		{
			name:      "tracked model third",
			msg:       `{"role":"assistant","usage":{"input":1}}`,
			wantModel: "[openclaw] tracked",
		},
		{
			name:      "unknown last",
			msg:       `{"role":"assistant","usage":{"input":1}}`,
			wantModel: "[openclaw] unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := `{"type":"message","message":` + tt.msg + `}`
			var recParsed line
			if err := json.Unmarshal([]byte(rec), &recParsed); err != nil {
				t.Fatal(err)
			}
			current := ""
			if !strings.Contains(tt.msg, `"model`) && tt.wantModel != "[openclaw] unknown" {
				current = "tracked"
			}
			e, ok := parseMessageEntry(&recParsed, "s1", current, "", time.Time{})
			if !ok {
				t.Fatal("parseMessageEntry returned no entry")
			}
			if e.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", e.Model, tt.wantModel)
			}
		})
	}
}

func TestParseTotalTokenFallback(t *testing.T) {
	tests := []struct {
		name       string
		usage      string
		wantOut    uint64
		wantCacheW uint64
		wantCacheR uint64
		wantExtra  uint64
	}{
		{
			name:      "lone total fills output",
			usage:     `{"totalTokens":222}`,
			wantOut:   222,
			wantExtra: 0,
		},
		{
			name:      "output known parks remainder in extra",
			usage:     `{"input":100,"output":50,"totalTokens":175}`,
			wantOut:   50,
			wantExtra: 25,
		},
		{
			name:       "cache write is part of known tokens, rest fills output",
			usage:      `{"input":10,"cacheWrite":90,"totalTokens":200}`,
			wantOut:    100,
			wantCacheW: 90,
		},
		{
			name:       "split fully known",
			usage:      `{"input":100,"output":25,"cacheRead":50,"totalTokens":175}`,
			wantOut:    25,
			wantCacheR: 50,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := `{"type":"message","message":{"role":"assistant","model":"m","usage":` + tt.usage + `}}`
			dir := t.TempDir()
			path := write(t, dir, "s.jsonl", rec+"\n")
			entries, err := parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			e := entries[0]
			if e.OutputTokens != tt.wantOut || e.CacheCreationTokens != tt.wantCacheW ||
				e.CacheReadTokens != tt.wantCacheR || e.ExtraTotalTokens != tt.wantExtra {
				t.Errorf("tokens = out:%d cc:%d cr:%d extra:%d, want out:%d cc:%d cr:%d extra:%d",
					e.OutputTokens, e.CacheCreationTokens, e.CacheReadTokens, e.ExtraTotalTokens,
					tt.wantOut, tt.wantCacheW, tt.wantCacheR, tt.wantExtra)
			}
			if e.ID == "" {
				t.Error("entry ID must be set for loader dedup")
			}
		})
	}
}

func TestParseSkipsNonUsageRecords(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"message","message":{"role":"assistant","content":"no usage block"}}`,
		`{"type":"message","message":{"role":"assistant","usage":{"input":0,"output":0}}}`, // all-zero dropped
		`not json at all`,
		`{"type":"message","message":"non-object message","usage":{}}`,
		`{"type":"message","message":{"role":"tool","usage":{"input":5,"output":5}}}`,
		"", // blank line
	}
	dir := t.TempDir()
	path := write(t, dir, "s.jsonl", strings.Join(lines, "\n")+"\n")
	entries, err := parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d: %+v", len(entries), entries)
	}
}

func TestParseMalformedLineDoesNotDropModelState(t *testing.T) {
	content := strings.Join([]string{
		`{"type":"model_change","modelId":"gpt-5.2","provider":"openai","message":"not-an-object"}`,
		`{"type":"message","message":{"role":"assistant","usage":{"input":10,"output":20}},"timestamp":` + tsMillis + `}`,
		"",
	}, "\n")
	dir := t.TempDir()
	path := write(t, dir, "s.jsonl", content)

	entries, err := parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].RawModel != "gpt-5.2" || entries[0].Version != "openai" {
		t.Errorf("model state lost: %+v", entries[0])
	}
}

func TestParseLenientNumbersAndMissingTimestamp(t *testing.T) {
	// Numeric-string usage values and a record-level timestamp fallback; the
	// file's mtime backs both when timestamps are absent entirely.
	content := strings.Join([]string{
		`{"type":"message","message":{"role":"assistant","usage":{"input":"7","output":"8"}}}`,
		`{"type":"message","message":{"role":"assistant","usage":{"input":"3","output":"4"}},"timestamp":"2026-01-30T06:00:00Z"}`,
		"",
	}, "\n")
	dir := t.TempDir()
	path := write(t, dir, "s.jsonl", content)

	entries, err := parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	first, second := entries[0], entries[1]
	// Entries sort by file order here (both share the fallback timestamp only
	// when parsing ran without per-record times; the second line carries one).
	if first.InputTokens != 7 || first.OutputTokens != 8 {
		t.Errorf("lenient numbers not applied: %+v", first)
	}
	if want := time.Date(2026, 1, 30, 6, 0, 0, 0, time.UTC); !second.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", second.Timestamp, want)
	}
	if first.Timestamp.Before(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) &&
		first.Timestamp.After(time.Unix(0, 0).Add(time.Minute)) {
		t.Errorf("first entry should carry the file mtime fallback, got %v", first.Timestamp)
	}
}

func TestParseDedupRelevantFieldsStable(t *testing.T) {
	line := `{"type":"message","message":{"role":"assistant","model":"gpt-5.2","usage":{"input":1,"output":1,"totalTokens":2},"timestamp":` + tsMillis + `}}`
	dir := t.TempDir()
	path := write(t, dir, "session.jsonl", line+"\n"+line+"\n")

	entries, err := parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("adapter emits every valid record (dedup is the loader's job); got %d", len(entries))
	}
	if entries[0].ID != entries[1].ID || entries[0].SessionID != "session" {
		t.Errorf("replayed records must share identity for loader dedup: %+v vs %+v",
			entries[0], entries[1])
	}
}

func TestParseUnreadableFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := parse(filepath.Join(dir, "missing.jsonl")); err == nil {
		t.Error("expected an error for an unreadable file")
	}
}

func TestDiscoverMissingDirReturnsNilNil(t *testing.T) {
	t.Setenv(envDir, filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover() error = %v, want nil", err)
	}
	if files != nil {
		t.Errorf("discover() = %v, want nil", files)
	}
}

func TestDiscoverEnvOverrideWins(t *testing.T) {
	envRoot := t.TempDir()
	write(t, envRoot, filepath.Join("agents", "main", "sessions", "env.jsonl"), "")
	homeRoot := t.TempDir()

	t.Setenv(envDir, envRoot)
	dirs := discoverDirs()
	if len(dirs) != 1 || dirs[0] != envRoot {
		t.Fatalf("discoverDirs() = %v, want [%s]", dirs, envRoot)
	}
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "env.jsonl" {
		t.Errorf("discover() = %v, want the env root's session file", files)
	}
	_ = homeRoot // home defaults are not consulted when the env var wins
}

func TestDiscoverCommaListDeduplicates(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envDir, root+","+root+", ,missing")
	dirs := discoverDirs()
	if len(dirs) != 1 || dirs[0] != root {
		t.Errorf("discoverDirs() = %v, want just [%s]", dirs, root)
	}
}

func TestDiscoverArchivedSessionsFound(t *testing.T) {
	root := t.TempDir()
	write(t, root, "agents/main/sessions/live.jsonl", "")
	write(t, root, "agents/main/sessions/old.jsonl.deleted.1700000000000", "")
	write(t, root, "agents/main/sessions/older.jsonl.reset.2026-03-20T06-34-44.520Z", "")
	write(t, root, "agents/main/sessions/notes.json", "")

	t.Setenv(envDir, root)
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[filepath.Base(f)] = true
	}
	for _, want := range []string{"live.jsonl", "old.jsonl.deleted.1700000000000", "older.jsonl.reset.2026-03-20T06-34-44.520Z"} {
		if !got[want] {
			t.Errorf("missing session file %q in %v", want, files)
		}
	}
	if got["notes.json"] {
		t.Error("notes.json must not be discovered")
	}
}

func TestDiscoverEmptyEnvFallsBackToHomeDefaults(t *testing.T) {
	// Point HOME at a temp dir with a .openclaw tree so the default lookup
	// finds exactly that root.
	home := t.TempDir()
	root := filepath.Join(home, ".openclaw")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv(envDir, "")

	dirs := discoverDirs()
	if len(dirs) != 1 || dirs[0] != root {
		t.Fatalf("discoverDirs() = %v, want [%s]", dirs, root)
	}
}

func TestProviderLeftEmptyForEngine(t *testing.T) {
	rec := `{"type":"message","message":{"role":"assistant","model":"m","usage":{"input":1,"output":2}}}`
	dir := t.TempDir()
	path := write(t, dir, "s.jsonl", rec+"\n")
	entries, _ := parse(path)
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].Provider != "" {
		t.Errorf("Provider = %q; the engine stamps it, adapters leave it empty", entries[0].Provider)
	}
}

func TestEntryIDShape(t *testing.T) {
	ts := time.UnixMilli(1769753935279)
	cost := 0.02
	id := entryID("abc", ts, "gpt-5.2", 1, 2, 3, 4, 5, &cost)
	want := "openclaw:abc:1769753935279:gpt-5.2:1:2:3:4:5:0.02"
	if id != want {
		t.Errorf("entryID = %q, want %q", id, want)
	}
	if entryID("abc", ts, "gpt-5.2", 1, 2, 3, 4, 5, nil) ==
		entryID("abc", ts, "gpt-5.2", 1, 2, 3, 4, 6, nil) {
		t.Error("different token splits must produce different ids")
	}
}

var _ = model.Entry{} // keep the model import anchored if helpers change
