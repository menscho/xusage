package droid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xusage/internal/model"
)

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

func TestLoadSettingsFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "session-a.settings.json", `{
		"model": "Claude-Sonnet-4-[Anthropic]",
		"providerLock": "anthropic",
		"providerLockTimestamp": "2026-05-01T01:02:03.000Z",
		"tokenUsage": {
			"inputTokens": 100,
			"outputTokens": 50,
			"cacheCreationTokens": 20,
			"cacheReadTokens": 10,
			"thinkingTokens": 5
		}
	}`)
	entries, err := loadSettingsFile(p)
	if err != nil {
		t.Fatalf("loadSettingsFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.SessionID != "session-a" {
		t.Errorf("SessionID = %q, want session-a", e.SessionID)
	}
	if e.Model != "claude-sonnet-4" {
		t.Errorf("Model = %q, want claude-sonnet-4 (normalized)", e.Model)
	}
	if e.InputTokens != 100 || e.OutputTokens != 50 ||
		e.CacheCreationTokens != 20 || e.CacheReadTokens != 10 {
		t.Errorf("token buckets wrong: %+v", e)
	}
	if e.ExtraTotalTokens != 5 {
		t.Errorf("ExtraTotalTokens (thinking) = %d, want 5", e.ExtraTotalTokens)
	}
	want := time.Date(2026, 5, 1, 1, 2, 3, 0, time.UTC)
	if !e.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, want)
	}
	if e.ID != "droid:session-a" {
		t.Errorf("ID = %q, want droid:session-a", e.ID)
	}
}

func TestParseTokenUsage_TotalFallback(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   tokenUsage
		wantOK bool
	}{
		{
			name:   "falls back to output tokens on lone total",
			raw:    `{"totalTokens": 456}`,
			want:   tokenUsage{OutputTokens: 456},
			wantOK: true,
		},
		{
			name:   "keeps explicit buckets, consumes covered total",
			raw:    `{"inputTokens": 10, "outputTokens": 20, "totalTokens": 30}`,
			want:   tokenUsage{InputTokens: 10, OutputTokens: 20},
			wantOK: true,
		},
		{
			name:   "excess over named buckets tops up thinking",
			raw:    `{"inputTokens": 10, "outputTokens": 20, "thinkingTokens": 5, "totalTokens": 60}`,
			want:   tokenUsage{InputTokens: 10, OutputTokens: 20, ThinkingTokens: 30},
			wantOK: true,
		},
		{
			name:   "all zero is rejected",
			raw:    `{"inputTokens": 0}`,
			wantOK: false,
		},
		{
			name:   "null usage rejected",
			raw:    `null`,
			wantOK: false,
		},
		{
			name:   "missing usage rejected",
			raw:    ``,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTokenUsage(json.RawMessage(tt.raw))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNormalizeModelName_Table(t *testing.T) {
	tests := []struct{ in, want string }{
		{"custom:Claude-Opus-4.5-Thinking-[Anthropic]-0", "claude-opus-4-5-thinking-0"},
		{"Claude-Sonnet-4-[Anthropic]", "claude-sonnet-4"},
		{"gemini-2.5-pro", "gemini-2-5-pro"},
		{"GPT 5.2", "gpt-5-2"},
		{"", ""},
		{"---", ""},
	}
	for _, tt := range tests {
		if got := normalizeModelName(tt.in); got != tt.want {
			t.Errorf("normalizeModelName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeProvider_Table(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "unknown"},
		{"claude", "anthropic"},
		{"Anthropic", "anthropic"},
		{"vertex-ai", "google"},
		{"x_ai", "xai"},
		{"grok", "xai"},
		{"mistral", "mistral"},
	}
	for _, tt := range tests {
		if got := normalizeProvider(tt.in); got != tt.want {
			t.Errorf("normalizeProvider(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestModelFromSidecarJSONL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "session-b.settings.json", `{}`)
	writeFile(t, dir, "session-b.jsonl",
		`{"other":"line"}
{"content":"Model: Claude Opus 4.5 Thinking [Anthropic]"}`)
	got := modelFromSidecarJSONL(filepath.Join(dir, "session-b.settings.json"))
	if got != "claude-opus-4-5-thinking" {
		t.Errorf("sidecar model = %q, want claude-opus-4-5-thinking", got)
	}
}

func TestLoadSettingsFile_FallsBackToSidecarAndInfersProvider(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "session-b.jsonl", `{"content":"Model: Claude Opus 4.5 [Anthropic]"}`)
	p := writeFile(t, dir, "session-b.settings.json", `{
		"providerLockTimestamp": "2026-05-02T01:02:03Z",
		"tokenUsage": {"inputTokens": 10, "outputTokens": 20}
	}`)
	entries, err := loadSettingsFile(p)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	if entries[0].Model != "claude-opus-4-5" {
		t.Errorf("Model = %q, want claude-opus-4-5", entries[0].Model)
	}
}

func TestLoadSettingsFile_KeepsLatestSnapshotForDuplicateSessionIDs(t *testing.T) {
	// Upstream keeps the newest snapshot per session ID across files; here each
	// file parses independently and dedup preference (higher total tokens) does
	// that job in the engine. The parser must produce identical IDs so the
	// engine can collapse them.
	dir := t.TempDir()
	p1 := writeFile(t, filepath.Join(dir, "archive"), "session-c.settings.json",
		`{"providerLockTimestamp":"2026-05-01T01:02:03Z","tokenUsage":{"inputTokens":10,"outputTokens":20}}`)
	p2 := writeFile(t, dir, "session-c.settings.json",
		`{"providerLockTimestamp":"2026-05-02T01:02:03Z","tokenUsage":{"inputTokens":100,"outputTokens":200}}`)
	e1, _ := loadSettingsFile(p1)
	e2, _ := loadSettingsFile(p2)
	if len(e1) != 1 || len(e2) != 1 {
		t.Fatalf("expected one entry per file")
	}
	if e1[0].ID != e2[0].ID {
		t.Errorf("IDs differ: %q vs %q — engine dedup could not collapse", e1[0].ID, e2[0].ID)
	}
	if e2[0].InputTokens != 100 {
		t.Errorf("newer snapshot should carry its own counts")
	}
}

func TestDiscover_MissingDirReturnsNilNoError(t *testing.T) {
	t.Setenv("DROID_SESSIONS_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover error = %v, want nil", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

func TestDiscover_FindsSettingsFilesViaEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.settings.json", "{}")
	writeFile(t, dir, "b.jsonl", "{}") // not a settings file
	sub := filepath.Join(dir, "nested")
	writeFile(t, sub, "c.settings.json", "{}")

	t.Setenv("DROID_SESSIONS_DIR", dir+", "+filepath.Join(dir, "missing"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want a+c settings files", files)
	}
	if !strings.HasSuffix(files[0], "a.settings.json") || !strings.HasSuffix(files[1], "c.settings.json") {
		t.Errorf("unexpected order or content: %v", files)
	}
}

func TestParse_SkipsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "broken.settings.json", "{not json")
	entries, err := parse(p)
	if err != nil {
		t.Fatalf("parse returned error for malformed file: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %v, want nil", entries)
	}
}

func TestSessionPaths_EnvWinsOverHomeDefault(t *testing.T) {
	fixture := t.TempDir()
	t.Setenv("DROID_SESSIONS_DIR", fixture)
	paths := sessionPaths()
	if len(paths) != 1 || paths[0] != fixture {
		t.Fatalf("sessionPaths = %v, want [%s]", paths, fixture)
	}
}

func TestEntryShape_DedupRelevantFieldsPresent(t *testing.T) {
	// The engine dedups on Provider+ID+RequestID and prefers higher totals;
	// droid entries must always carry a stable ID and full token buckets.
	var e model.Entry
	_ = e.TotalTokens // compile-time shape check alongside runtime assertions
	dir := t.TempDir()
	p := writeFile(t, dir, "s.settings.json",
		`{"model":"gpt-5","providerLock":"openai","providerLockTimestamp":"2026-05-03T00:00:00Z",
		  "tokenUsage":{"inputTokens":1,"outputTokens":2,"cacheCreationTokens":3,"cacheReadTokens":4,"thinkingTokens":5}}`)
	entries, err := parse(p)
	if err != nil || len(entries) != 1 {
		t.Fatalf("parse: %v", err)
	}
	e = entries[0]
	if e.ID == "" || e.SessionID == "" || e.Provider != "" {
		t.Errorf("dedup fields wrong (Provider must stay empty; engine sets it): %+v", e)
	}
	if got := e.TotalTokens(); got != 15 {
		t.Errorf("TotalTokens = %d, want 15", got)
	}
}

func TestEntryCarriesProjectLabel(t *testing.T) {
	// Upstream stamps every droid entry with project "droid".
	dir := t.TempDir()
	p := writeFile(t, dir, "s.settings.json",
		`{"model":"gpt-5","tokenUsage":{"inputTokens":1,"outputTokens":2}}`)
	entries, err := parse(p)
	if err != nil || len(entries) != 1 {
		t.Fatalf("parse: %v", err)
	}
	if entries[0].Project != "droid" {
		t.Errorf("Project = %q, want %q", entries[0].Project, "droid")
	}
}

func TestDiscoverRecursiveNestedArchives(t *testing.T) {
	// Upstream collect_files_with_extension recurses fully; nested archive
	// directories must be scanned too.
	dir := t.TempDir()
	writeFile(t, dir, "a.settings.json", "{}")
	writeFile(t, dir, "deep/nested/b.settings.json", "{}")
	writeFile(t, dir, "deep/not-settings.json", "{}")

	t.Setenv("DROID_SESSIONS_DIR", dir)
	files, err := discover()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want a + nested b settings files", files)
	}
}

func TestSidecarNotConsultedWhenModelNormalizesEmpty(t *testing.T) {
	// Upstream reads the sidecar only when the settings file carries no model;
	// a model that normalizes to "" falls back to the provider default.
	dir := t.TempDir()
	writeFile(t, dir, "s.jsonl", `{"content":"Model: Claude Opus 4.5 [Anthropic]"}`)
	p := writeFile(t, dir, "s.settings.json", `{
		"model": "---",
		"providerLock": "anthropic",
		"providerLockTimestamp": "2026-05-01T01:02:03Z",
		"tokenUsage": {"inputTokens": 1, "outputTokens": 2}
	}`)
	entries, err := parse(p)
	if err != nil || len(entries) != 1 {
		t.Fatalf("parse: %v", err)
	}
	if entries[0].Model != "claude-unknown" {
		t.Errorf("Model = %q, want claude-unknown (provider default, not sidecar)", entries[0].Model)
	}
}

func TestSessionPathsSetButEmptyEnvWinsEntirely(t *testing.T) {
	// A set-but-empty DROID_SESSIONS_DIR suppresses the home default entirely,
	// mirroring upstream env-wins semantics.
	t.Setenv("DROID_SESSIONS_DIR", "")
	if paths := sessionPaths(); len(paths) != 0 {
		t.Errorf("sessionPaths = %v, want empty", paths)
	}
}
