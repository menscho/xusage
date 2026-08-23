package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/model"
)

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

// mustParseDir walks a fixture config dir the way Discover+Parse would and
// returns every parsed entry.
func mustParseDir(t *testing.T, dir string) []model.Entry {
	t.Helper()
	files, err := usageFilesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	var entries []model.Entry
	for _, f := range files {
		es, err := Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, es...)
	}
	return entries
}

const baseLine = `{"timestamp":"2026-03-29T07:00:00.000Z","version":"1.2.3","sessionId":"s1",` +
	`"requestId":"req-1","cwd":"/home/me/proj","costUSD":0.01,` +
	`"message":{"id":"msg-1","model":"claude-sonnet-4-20250514",` +
	`"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":5,` +
	`"cache_read_input_tokens":7}}}`

func TestParseHappyPath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "projects/myproj/s1.jsonl", baseLine+"\n")
	entries := mustParseDir(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.ID != "msg-1" || e.RequestID != "req-1" || e.SessionID != "s1" {
		t.Errorf("ids wrong: %+v", e)
	}
	if e.Project != "myproj" {
		t.Errorf("project = %q, want myproj", e.Project)
	}
	if e.Model != "claude-sonnet-4-20250514" || e.RawModel != e.Model {
		t.Errorf("model = %q raw = %q", e.Model, e.RawModel)
	}
	if e.InputTokens != 10 || e.OutputTokens != 20 || e.CacheCreationTokens != 5 || e.CacheReadTokens != 7 {
		t.Errorf("tokens wrong: %+v", e)
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.01 {
		t.Errorf("reported cost = %v", e.ReportedCost)
	}
	want := time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC)
	if !e.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", e.Timestamp, want)
	}
	if e.Version != "1.2.3" {
		t.Errorf("version = %q", e.Version)
	}
}

func TestParseCacheCreationSplit(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","sessionId":"s","requestId":"r",` +
		`"message":{"id":"m","model":"claude-opus-4-20250514","usage":{` +
		`"input_tokens":1,"output_tokens":2,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":50},` +
		`"cache_read_input_tokens":0}}}`
	write(t, dir, "projects/p/s.jsonl", line+"\n")
	entries := mustParseDir(t, dir)
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].CacheCreationTokens != 100 || entries[0].CacheCreation1h != 50 {
		t.Errorf("split wrong: 5m=%d 1h=%d", entries[0].CacheCreationTokens, entries[0].CacheCreation1h)
	}
}

func TestParseFastSpeedSuffix(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","sessionId":"s","requestId":"r",` +
		`"message":{"id":"m","model":"claude-opus-4-20250514","usage":{` +
		`"input_tokens":1,"output_tokens":2,"speed":"fast","cache_read_input_tokens":0}}}`
	write(t, dir, "projects/p/s.jsonl", line+"\n")
	entries := mustParseDir(t, dir)
	if entries[0].Model != "claude-opus-4-20250514-fast" {
		t.Errorf("model = %q, want -fast suffix", entries[0].Model)
	}
	if entries[0].RawModel != "claude-opus-4-20250514" {
		t.Errorf("raw model = %q", entries[0].RawModel)
	}
	if !entries[0].SpeedFast {
		t.Error("SpeedFast not set")
	}
}

func TestParseSyntheticSkipped(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","sessionId":"s","requestId":"r",` +
		`"message":{"id":"m","model":"<synthetic>","usage":{"input_tokens":9,` +
		`"output_tokens":9,"cache_read_input_tokens":0}}}`
	write(t, dir, "projects/p/s.jsonl", line+"\n")
	entries := mustParseDir(t, dir)
	if len(entries) != 0 {
		t.Errorf("synthetic entry not skipped: %+v", entries)
	}
}

func TestParseNullFieldRejection(t *testing.T) {
	cases := map[string]string{
		"speed null":        `{"sessionId":"s","message":{"model":"m","usage":{"speed":null,"input_tokens":1}}}`,
		"model null":        `{"sessionId":"s","message":{"model":null,"usage":{"input_tokens":1}}}`,
		"sessionId null":    `{"sessionId":null,"message":{"model":"m","usage":{"input_tokens":1}}}`,
		"cache read null":   `{"sessionId":"s","message":{"model":"m","usage":{"cache_read_input_tokens":null}}}`,
		"cache create null": `{"sessionId":"s","message":{"model":"m","usage":{"cache_creation_input_tokens":null}}}`,
		"content null ok":   `{"sessionId":"s","message":{"content":null,"model":"m","usage":{"input_tokens":1,"cache_read_input_tokens":0}}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			got := hasUnsupportedNullField(line)
			want := name != "content null ok"
			if got != want {
				t.Errorf("hasUnsupportedNullField = %v, want %v", got, want)
			}
		})
	}
}

func TestParseMalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	content := "not json at all\n" +
		baseLine + "\n" +
		`{"timestamp":"nope","sessionId":"s","message":{"id":"m","model":"m","usage":{"input_tokens":1,"cache_read_input_tokens":0}}}` + "\n" +
		"\n"
	write(t, dir, "projects/p/s.jsonl", content)
	entries := mustParseDir(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected only the valid line to survive, got %d", len(entries))
	}
}

func TestParseSemverCheck(t *testing.T) {
	cases := []struct {
		version string
		valid   bool
	}{
		{"1.2.3", true},
		{"1.2.3-beta.1", true},
		{"10.0.1", true},
		{"abc", false},
		{"1.2", false},
		{"x.y.z", false},
	}
	for _, tc := range cases {
		if got := isSemverPrefix(tc.version); got != tc.valid {
			t.Errorf("isSemverPrefix(%q) = %v, want %v", tc.version, got, tc.valid)
		}
	}
}

func TestParseEmptyStringRejection(t *testing.T) {
	line := `{"timestamp":"2026-03-29T07:00:00.000Z","sessionId":"","requestId":"r",` +
		`"message":{"id":"m","model":"m","usage":{"input_tokens":1,"cache_read_input_tokens":0}}}`
	_, ok := parseLine(line, "/home/me/.claude/projects/p/s.jsonl")
	if ok {
		t.Error("empty sessionId should reject the line")
	}
}

func TestSessionExtraction(t *testing.T) {
	cases := []struct {
		path                string
		sessionID, projectP string
	}{
		{"/home/me/.claude/projects/project-a/session-a.jsonl", "session-a", "project-a"},
		{"/home/me/.claude/projects/project-a/session-a/chat.jsonl", "session-a", "project-a"},
		{"/home/me/.claude/projects/project-a/session-a/subagents/worker.jsonl", "session-a", "project-a"},
	}
	for _, tc := range cases {
		s, p := extractSessionParts(tc.path)
		if s != tc.sessionID || p != tc.projectP {
			t.Errorf("extractSessionParts(%q) = (%q,%q), want (%q,%q)",
				tc.path, s, p, tc.sessionID, tc.projectP)
		}
	}
	if got := extractProject("/h/.claude/projects/proj-x/s/a.jsonl"); got != "proj-x" {
		t.Errorf("extractProject = %q", got)
	}
	if got := extractProject("/somewhere/else.jsonl"); got != "unknown" {
		t.Errorf("extractProject fallback = %q", got)
	}
}

func TestDiscoverMissingDirReturnsNil(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "missing"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover error = %v, want nil", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

func TestDiscoverEnvOverride(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "projects/p/s.jsonl", baseLine+"\n")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file under env override, got %d", len(files))
	}
}

func TestUsageLimitReset(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-03-29T07:00:00.000Z","isApiErrorMessage":true,` +
		`"sessionId":"s","message":{"id":"m","model":"m",` +
		`"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0}},` +
		`"text":"Claude AI usage limit reached (5 hour limit) | 1775000000"}`
	reset, ok := parseLine(line, "/home/me/.claude/projects/p/s.jsonl")
	if !ok {
		t.Fatal("line rejected")
	}
	if reset[0].UsageLimitReset == nil {
		t.Fatal("UsageLimitReset not extracted")
	}
	want := time.Unix(1775000000, 0).UTC()
	if !reset[0].UsageLimitReset.Equal(want) {
		t.Errorf("reset = %v, want %v", reset[0].UsageLimitReset, want)
	}
}

// Upstream ports each message.usage.iterations[] record of type
// "advisor_message" as its own entry under the advisor model, with the id
// suffixed ":advisor:<n>" and no reported cost.
func TestParseAdvisorIterations(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-05-22T02:34:40.000Z","version":"1.2.3","sessionId":"session-a",` +
		`"message":{"id":"msg-parent","model":"main-model","usage":{"input_tokens":1,"output_tokens":2,` +
		`"iterations":[{"type":"advisor_message","model":"advisor-model","input_tokens":10,` +
		`"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},` +
		`{"type":"other","model":"skip-me","input_tokens":5,"output_tokens":5}]}},` +
		`"requestId":"req-parent","costUSD":1.23}`
	write(t, dir, "projects/project-a/session-a/chat.jsonl", line+"\n")
	entries := mustParseDir(t, dir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	main, advisor := entries[0], entries[1]
	if main.ID != "msg-parent" || main.Model != "main-model" || main.ReportedCost == nil {
		t.Errorf("main entry wrong: %+v", main)
	}
	if advisor.ID != "msg-parent:advisor:0" {
		t.Errorf("advisor id = %q", advisor.ID)
	}
	if advisor.Model != "advisor-model" || advisor.RawModel != "advisor-model" {
		t.Errorf("advisor model = %q raw %q", advisor.Model, advisor.RawModel)
	}
	if advisor.InputTokens != 10 || advisor.OutputTokens != 2 || advisor.CacheReadTokens != 0 {
		t.Errorf("advisor tokens wrong: %+v", advisor)
	}
	if advisor.ReportedCost != nil {
		t.Errorf("advisor must carry no reported cost: %v", advisor.ReportedCost)
	}
	if advisor.SessionID != "session-a" || advisor.SessionID != main.SessionID {
		t.Errorf("advisor session = %q", advisor.SessionID)
	}
}

// With no CLAUDE_CONFIG_DIR, upstream checks <xdg>/claude where <xdg> is
// $XDG_CONFIG_HOME or ~/.config when unset.
func TestDiscoverXDGDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	write(t, filepath.Join(home, ".config"), "claude/projects/p/s.jsonl", baseLine+"\n")
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file under ~/.config/claude, got %d: %v", len(files), files)
	}
}

func TestDiscoverXDGRConfigHomeOverride(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	write(t, xdg, "claude/projects/p/s.jsonl", baseLine+"\n")
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file under $XDG_CONFIG_HOME/claude, got %d: %v", len(files), files)
	}
}
