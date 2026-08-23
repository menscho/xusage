package grok

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// writeSession materializes one session directory under a GROK_HOME fixture.
func writeSession(t *testing.T, root, project, session string, updates string, summary string) string {
	t.Helper()
	dir := filepath.Join(root, "sessions", project, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte(updates), 0o644); err != nil {
		t.Fatal(err)
	}
	if summary != "" {
		if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(summary), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// withGrokHome points the adapter at a fixture root for one test.
func withGrokHome(t *testing.T, root string) {
	t.Helper()
	t.Setenv("GROK_HOME", root)
}

// mustParse parses a file and fails the test on error.
func mustParse(t *testing.T, path string) []model.Entry {
	t.Helper()
	entries, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile(%q): %v", path, err)
	}
	return entries
}

func TestParseTurnCompletedModelUsage(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	line := `{"timestamp":1750000000,"method":"_x.ai/session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"turn_completed","usage":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"reasoningTokens":10,"totalTokens":120,"modelUsage":{"grok-4.5-build":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"reasoningTokens":10,"totalTokens":120}}}},"_meta":{"eventId":"evt-1"}}}`
	writeSession(t, root, "proj", "sess-1", line+"\n", "")

	entries := mustParse(t, filepath.Join(root, "sessions", "proj", "sess-1", "updates.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	// cachedReadTokens is a subset of inputTokens: uncached = 100-40.
	if e.InputTokens != 60 {
		t.Errorf("InputTokens = %d, want 60", e.InputTokens)
	}
	if e.CacheReadTokens != 40 {
		t.Errorf("CacheReadTokens = %d, want 40", e.CacheReadTokens)
	}
	if e.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", e.OutputTokens)
	}
	if e.ExtraTotalTokens != 0 {
		t.Errorf("ExtraTotalTokens = %d, want 0 (reasoning already inside output)", e.ExtraTotalTokens)
	}
	if e.Model != "grok-4.5-build" {
		t.Errorf("Model = %q, want grok-4.5-build", e.Model)
	}
	if e.ID != "evt-1" || e.RequestID != "evt-1" {
		t.Errorf("ID/RequestID = %q/%q, want evt-1", e.ID, e.RequestID)
	}
	if e.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", e.SessionID)
	}
	if e.Project != "proj" {
		t.Errorf("Project = %q, want proj (URL-decoded path segment)", e.Project)
	}
	want := time.Unix(1750000000, 0).UTC()
	if !e.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, want)
	}
}

func TestParseTopLevelFallbackUsesSummaryDefaultModel(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	line := `{"timestamp":1750000000,"params":{"sessionId":"sess-top","update":{"sessionUpdate":"turn_completed","usage":{"inputTokens":50,"outputTokens":5,"cachedReadTokens":10,"reasoningTokens":2}},"_meta":{"eventId":"evt-top"}}}`
	writeSession(t, root, "proj", "sess-top",
		line+"\n",
		`{"info":{"id":"sess-top","cwd":"/tmp/proj"},"current_model_id":"grok-4.5-build"}`)

	entries := mustParse(t, filepath.Join(root, "sessions", "proj", "sess-top", "updates.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Model != "grok-4.5-build" {
		t.Errorf("Model = %q, want summary current_model_id", e.Model)
	}
	if e.InputTokens != 40 { // 50 - 10 cached
		t.Errorf("InputTokens = %d, want 40", e.InputTokens)
	}
	if e.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens = %d, want 10", e.CacheReadTokens)
	}
	if e.Project != "/tmp/proj" {
		t.Errorf("Project = %q, want summary cwd", e.Project)
	}
}

func TestParseUnknownModelWithoutSummary(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	line := `{"timestamp":1750000000,"params":{"sessionId":"sess-u","update":{"sessionUpdate":"turn_completed","usage":{"inputTokens":10,"outputTokens":1}},"_meta":{"eventId":"evt-u"}}}`
	writeSession(t, root, "proj", "sess-u", line+"\n", "")
	entries := mustParse(t, filepath.Join(root, "sessions", "proj", "sess-u", "updates.jsonl"))
	if len(entries) != 1 || entries[0].Model != "unknown" {
		t.Fatalf("Model = %q (n=%d), want unknown", entries[0].Model, len(entries))
	}
}

func TestParseCostUsdTicks(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	// Verbatim tick count from upstream tests: 185192000 ticks = $0.0185192.
	line := `{"timestamp":1750000000,"params":{"sessionId":"sess-1","update":{"sessionUpdate":"turn_completed","usage":{"costUsdTicks":185192000,"modelUsage":{"grok-4.5-build":{"inputTokens":18444,"outputTokens":130,"cachedReadTokens":11264,"reasoningTokens":73,"costUsdTicks":185192000}}}},"_meta":{"eventId":"evt-cost"}}}`
	writeSession(t, root, "proj", "sess-cost", line+"\n", "")
	entries := mustParse(t, filepath.Join(root, "sessions", "proj", "sess-cost", "updates.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].ReportedCost == nil {
		t.Fatal("ReportedCost is nil, want 0.0185192")
	}
	if diff := *entries[0].ReportedCost - 0.0185192; diff < -1e-12 || diff > 1e-12 {
		t.Errorf("ReportedCost = %v, want 0.0185192", *entries[0].ReportedCost)
	}
}

func TestParseCacheCreationSplit(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	// cacheCreation carves out of uncached input: 100-40 read-25 create = 35.
	line := `{"timestamp":1750000000,"params":{"sessionId":"sess-1","update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"grok-4.5-build":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"cacheCreationTokens":25,"reasoningTokens":10}}}},"_meta":{"eventId":"evt-cc"}}}`
	writeSession(t, root, "proj", "sess-cc", line+"\n", "")
	entries := mustParse(t, filepath.Join(root, "sessions", "proj", "sess-cc", "updates.jsonl"))
	e := entries[0]
	if e.InputTokens != 35 || e.CacheReadTokens != 40 || e.CacheCreationTokens != 25 {
		t.Errorf("split = (%d, read %d, create %d), want (35, 40, 25)",
			e.InputTokens, e.CacheReadTokens, e.CacheCreationTokens)
	}
}

func TestParseTimestampPreference(t *testing.T) {
	tests := []struct {
		name string
		line string
		want time.Time
	}{
		{name: "agent ms wins over envelope seconds",
			line: `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1}}}},"_meta":{"eventId":"a","agentTimestampMs":1785328986355}}}`,
			want: time.UnixMilli(1785328986355).UTC()},
		{name: "envelope unix seconds",
			line: `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1}}}},"_meta":{"eventId":"b"}}}`,
			want: time.Unix(1750000000, 0).UTC()},
		{name: "epoch when absent",
			line: `{"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1}}}},"_meta":{"eventId":"c"}}}`,
			want: time.Unix(0, 0).UTC()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "updates.jsonl")
			if err := os.WriteFile(path, []byte(tt.line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			entries := mustParse(t, path)
			if len(entries) != 1 || !entries[0].Timestamp.Equal(tt.want) {
				got := time.Time{}
				if len(entries) == 1 {
					got = entries[0].Timestamp
				}
				t.Fatalf("Timestamp = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseSummaryOverridesPathDerivedMeta(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	// No params.sessionId: summary info.id should supply it; line id wins when present.
	line := `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"grok-4.5-build":{"inputTokens":10,"outputTokens":1}}}},"_meta":{"eventId":"evt-meta"}}}`
	writeSession(t, root, "D%3A%5Cwork%5Cproj", "019fa1b1-0000-7000-8000-000000000001",
		line+"\n",
		`{"info":{"id":"canonical-session","cwd":"D:\\canonical\\cwd"},"git_root_dir":"should-not-win"}`)
	entries := mustParse(t, filepath.Join(root, "sessions", "D%3A%5Cwork%5Cproj",
		"019fa1b1-0000-7000-8000-000000000001", "updates.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].SessionID != "canonical-session" {
		t.Errorf("SessionID = %q, want canonical-session from summary", entries[0].SessionID)
	}
	if entries[0].Project != `D:\canonical\cwd` {
		t.Errorf("Project = %q, want summary cwd", entries[0].Project)
	}
}

func TestParseURLEncodedProjectSegment(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	line := `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"grok-4.5-build":{"inputTokens":10,"outputTokens":1}}}},"_meta":{"eventId":"evt-url"}}}`
	writeSession(t, root, "D%3A%5Cwork%5Cproj", "sess-url", line+"\n", "")
	entries := mustParse(t, filepath.Join(root, "sessions", "D%3A%5Cwork%5Cproj", "sess-url", "updates.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if got := entries[0].Project; got != `D:\work\proj` {
		t.Errorf("Project = %q, want D:\\work\\proj", got)
	}
	if got := entries[0].SessionID; got != "sess-url" {
		t.Errorf("SessionID = %q, want directory name fallback", got)
	}
}

func TestParseSkipsNonTurnAndZeroRows(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	lines := strings.Join([]string{
		`{"timestamp":1750000001,"params":{"update":{"sessionUpdate":"tool_call"}}}`,
		`{"timestamp":1750000002,"params":{"update":{"sessionUpdate":"turn_completed"},"_meta":{"eventId":"no-usage"}}}`,
		`{"timestamp":1750000003,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"grok-4.5":{"inputTokens":0,"outputTokens":0,"cachedReadTokens":0,"reasoningTokens":0}}}},"_meta":{"eventId":"zero"}}}`,
		`{"this is not json`,
		``,
		`{"timestamp":1750000000,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"grok-4.5-build":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":40,"reasoningTokens":10}}}},"_meta":{"eventId":"ok"}}}`,
	}, "\n")
	writeSession(t, root, "proj", "sess-skip", lines+"\n", "")
	entries := mustParse(t, filepath.Join(root, "sessions", "proj", "sess-skip", "updates.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].InputTokens != 60 {
		t.Errorf("InputTokens = %d, want 60", entries[0].InputTokens)
	}
}

func TestParseDedupRules(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{name: "same eventId and model collapses",
			lines: []string{sampleLine("evt-dup"), sampleLine("evt-dup")}, want: 1},
		{name: "same eventId different models both kept",
			lines: []string{
				sampleLineWithModel("evt-shared", "model-a"),
				sampleLineWithModel("evt-shared", "model-b"),
			}, want: 2},
		{name: "identical rows without eventId collapse by content key",
			lines: []string{contentLine(100, 20, 40), contentLine(100, 20, 40)}, want: 1},
		{name: "rows without eventId differing only in cache creation kept",
			lines: []string{contentLine(100, 5, 0), contentLine(80, 5, 0)}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "updates.jsonl")
			content := ""
			for _, l := range tt.lines {
				content += l + "\n"
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			entries := mustParse(t, path)
			if len(entries) != tt.want {
				t.Fatalf("got %d entries, want %d", len(entries), tt.want)
			}
		})
	}
}

func sampleLine(eventID string) string {
	return sampleLineWithModel(eventID, "grok-4.5-build")
}

func sampleLineWithModel(eventID, model string) string {
	rec := map[string]any{
		"timestamp": 1750000100,
		"params": map[string]any{
			"sessionId": "sess-m",
			"update": map[string]any{
				"sessionUpdate": "turn_completed",
				"usage": map[string]any{
					"modelUsage": map[string]any{
						model: map[string]any{
							"inputTokens": 100, "outputTokens": 20,
							"cachedReadTokens": 40, "reasoningTokens": 10,
						},
					},
				},
			},
			"_meta": map[string]any{"eventId": eventID},
		},
	}
	data, _ := json.Marshal(rec)
	return string(data)
}

func contentLine(input, output, cacheRead int64) string {
	rec := map[string]any{
		"timestamp": 1750000000,
		"params": map[string]any{
			"sessionId": "sess-d",
			"update": map[string]any{
				"sessionUpdate": "turn_completed",
				"usage": map[string]any{
					"modelUsage": map[string]any{
						"grok-4.5-build": map[string]any{
							"inputTokens": input, "outputTokens": output,
							"cachedReadTokens": cacheRead,
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(rec)
	return string(data)
}

func TestParseMultiModelTurnEmitsOneEntryPerModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates.jsonl")
	line := `{"timestamp":1750000100,"params":{"sessionId":"sess-multi","update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"model-b":{"inputTokens":20,"outputTokens":4,"cachedReadTokens":5},"model-a":{"inputTokens":10,"outputTokens":2}}}},"_meta":{"eventId":"evt-multi"}}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := mustParse(t, path)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	// Deterministic order: sorted by model name.
	if entries[0].Model != "model-a" || entries[1].Model != "model-b" {
		t.Errorf("models = [%s, %s], want [model-a model-b]", entries[0].Model, entries[1].Model)
	}
	if entries[1].InputTokens != 15 || entries[1].CacheReadTokens != 5 {
		t.Errorf("model-b split = (%d, %d), want (15, 5)", entries[1].InputTokens, entries[1].CacheReadTokens)
	}
}

func TestDiscoverFindsOnlyUpdatesJSONL(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	writeSession(t, root, "proj-a", "sess-1", "{}\n", "{}")
	writeSession(t, root, "proj-b", "sess-2", "{}\n", "")
	other := filepath.Join(root, "sessions", "proj-a", "sess-1", "events.jsonl")
	if err := os.WriteFile(other, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(root, "logs", "unified.jsonl")
	if err := os.MkdirAll(filepath.Dir(logs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logs, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := discoverSessionFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("discovered %d files, want 2: %v", len(files), files)
	}
	for _, f := range files {
		if filepath.Base(f) != "updates.jsonl" {
			t.Errorf("unexpected file %q", f)
		}
	}
	if files[0] >= files[1] {
		t.Errorf("files not sorted: %v", files)
	}
}

func TestDiscoverMissingRootReturnsNilNil(t *testing.T) {
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := discoverSessionFiles()
	if err != nil {
		t.Fatalf("missing root returned error %v, want nil", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

func TestDiscoverNoEnvFallsBackToHomeDotGrok(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.Unsetenv("GROK_HOME")
	dotGrok := filepath.Join(home, ".grok")
	writeSession(t, dotGrok, "proj", "sess-h", "{}\n", "")

	files, err := discoverSessionFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "updates.jsonl" {
		t.Fatalf("files = %v, want one updates.jsonl under ~/.grok/sessions", files)
	}
}

func TestRegistryShape(t *testing.T) {
	// The package registers itself under the exact source name via init().
	for _, s := range sources.All() {
		if s.Name == "grok" {
			if s.Discover == nil || s.Parse == nil {
				t.Fatal("Discover/Parse must be non-nil")
			}
			return
		}
	}
	t.Fatal("grok source not registered")
}

func TestURLDecodeLightweight(t *testing.T) {
	tests := []struct{ in, want string }{
		{"D%3A%5Cwork%5Cproj", `D:\work\proj`},
		{"%2Ftmp%2F%E6%97%A5%E6%9C%AC", "/tmp/日本"},
		{"%2Ftmp%2F%FFx", "/tmp/\uFFFDx"},
		{"plain", "plain"},
		{"trailing%", "trailing%"},
		{"bad%ZZ", "bad%ZZ"},
	}
	for _, tt := range tests {
		if got := urlDecodeLightweight(tt.in); got != tt.want {
			t.Errorf("urlDecodeLightweight(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
