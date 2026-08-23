package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

func tokenCount(ts string, total rawTokens, last *rawTokens) string {
	b, _ := json.Marshal(map[string]any{
		"timestamp": ts,
		"type":      "event_msg",
		"payload": map[string]any{
			"type": "token_count",
			"info": map[string]any{
				"model":             "gpt-5.2",
				"total_token_usage": total,
				"last_token_usage":  last,
			},
		},
	})
	return string(b)
}

func TestParseHappyPath(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"timestamp":"2026-05-12T08:00:00.000Z","type":"session_meta","payload":{"cwd":"/home/me/myproj"}}`,
		`{"timestamp":"2026-05-12T08:00:01.000Z","type":"turn_context","payload":{"model":"gpt-5.2"}}`,
		tokenCount("2026-05-12T08:01:00.000Z", rawTokens{InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 200, ReasoningOutTokens: 20, TotalTokens: 1200}, nil),
	}
	write(t, dir, "sessions/2026/05/12/r1.jsonl", join(lines))
	entries := mustParse(t, dir)
	if len(entries) != 1 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.SessionID == "" || e.Project != "myproj" {
		t.Errorf("session/project wrong: %+v", e)
	}
	if e.Model != "gpt-5.2" {
		t.Errorf("model = %q", e.Model)
	}
	if e.CacheReadTokens != 100 {
		t.Errorf("cached -> cache read = %d", e.CacheReadTokens)
	}
	if e.InputTokens != 900 { // input minus cached
		t.Errorf("input = %d, want 900", e.InputTokens)
	}
	if e.OutputTokens != 220 { // reasoning (20) folds into output by summation
		t.Errorf("output = %d, want 220", e.OutputTokens)
	}
}

func TestParseCollapseConsecutiveTokenCounts(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		tokenCount("2026-05-12T08:01:00.000Z", rawTokens{InputTokens: 1000, OutputTokens: 200, TotalTokens: 1200}, nil),
		// Repeated identical total: no new usage, must be dropped.
		tokenCount("2026-05-12T08:02:00.000Z", rawTokens{InputTokens: 1000, OutputTokens: 200, TotalTokens: 1200}, nil),
		// Advanced total: contributes only the delta.
		tokenCount("2026-05-12T08:03:00.000Z", rawTokens{InputTokens: 1600, CachedInputTokens: 300, OutputTokens: 450, TotalTokens: 2050}, nil),
	}
	write(t, dir, "sessions/s.jsonl", join(lines))
	entries := mustParse(t, dir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (repeat collapsed): %+v", len(entries), entries)
	}
	last := entries[len(entries)-1]
	// The delta's input_tokens (600) still contains the newly cached tokens
	// (300), so the non-cached input is 300 and cache read is 300.
	if last.InputTokens != 300 || last.OutputTokens != 250 || last.CacheReadTokens != 300 {
		t.Errorf("delta wrong: in=%d out=%d read=%d", last.InputTokens, last.OutputTokens, last.CacheReadTokens)
	}
}

func TestParseTotalOnlyBecomesExtraTotal(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-05-12T08:01:00.000Z","type":"event_msg","payload":{"type":"token_count",` +
		`"info":{"total_token_usage":{"total_tokens":777}}}}`
	write(t, dir, "sessions/s.jsonl", line)
	entries := mustParse(t, dir)
	if len(entries) != 1 || entries[0].ExtraTotalTokens != 777 {
		t.Errorf("entries = %+v, want lone ExtraTotalTokens=777", entries)
	}
}

func TestParseMalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	content := "garbage\n{broken\n" +
		tokenCount("2026-05-12T08:01:00.000Z", rawTokens{InputTokens: 10, TotalTokens: 10}, nil) + "\n"
	write(t, dir, "sessions/s.jsonl", content)
	entries := mustParse(t, dir)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
}

func TestParseNumericTimestamp(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":1767312001000,"type":"event_msg","payload":{"type":"token_count",` +
		`"info":{"total_token_usage":{"input_tokens":100,"total_tokens":150}}}}`
	write(t, dir, "sessions/s.jsonl", line)
	entries := mustParse(t, dir)
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	if got := entries[0].Timestamp.UnixMilli(); got != 1767312001000 {
		t.Errorf("ts millis = %d", got)
	}
}

func TestDiscoverMissingDirReturnsNil(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "missing"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover error = %v", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
}

func TestDiscoverSessionsAndArchived(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sessions/a.jsonl", "{}\n")
	write(t, dir, "archived_sessions/b.jsonl", "{}\n")
	t.Setenv("CODEX_HOME", dir)
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(files), files)
	}
}

func TestProjectFromCWD(t *testing.T) {
	cases := map[string]string{
		"/home/me/proj":    "proj",
		"C:\\dev\\MyApp\\": "MyApp",
		"/":                "",
		"":                 "",
	}
	for cwd, want := range cases {
		if got := projectFromCWD(cwd); got != want {
			t.Errorf("projectFromCWD(%q) = %q, want %q", cwd, got, want)
		}
	}
}

// settingsLine renders a thread_settings_applied event_msg record.
func settingsLine(ts string, inner string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{` + inner + `}}}`
}

func lastCount(ts string, in, cached, out, reasoning uint64) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{` +
		`"model":"gpt-5.2","last_token_usage":{"input_tokens":` + itou(in) +
		`,"cached_input_tokens":` + itou(cached) +
		`,"output_tokens":` + itou(out) +
		`,"reasoning_output_tokens":` + itou(reasoning) +
		`,"total_tokens":` + itou(in+cached+out+reasoning) + `}}}}`
}

func itou(n uint64) string {
	return strconv.FormatUint(n, 10)
}

// Upstream prefers last_token_usage over diffing cumulative totals whenever
// the cumulative total advanced; the delta would double-count nothing here,
// but the two shapes differ when a turn's cache share shifted.
func TestParsePrefersLastTokenUsage(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"timestamp":"2026-05-12T08:00:00.000Z","type":"session_meta","payload":{"cwd":"/home/me/proj"}}`,
		tokenCount("2026-05-12T08:01:00.000Z", rawTokens{InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 200, ReasoningOutTokens: 20, TotalTokens: 1320}, nil),
		lastCount("2026-05-12T08:02:00.000Z", 500, 50, 60, 0),
	}
	write(t, dir, "sessions/s.jsonl", join(lines))
	entries := mustParse(t, dir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	e := entries[1]
	if e.InputTokens != 450 || e.CacheReadTokens != 50 || e.OutputTokens != 60 {
		t.Errorf("last_token_usage not preferred: in=%d read=%d out=%d", e.InputTokens, e.CacheReadTokens, e.OutputTokens)
	}
	if e.Project != "proj" {
		t.Errorf("project = %q, want proj (session_meta still tracked)", e.Project)
	}
}

func TestParseRepeatedCumulativeTotalCollapsed(t *testing.T) {
	dir := t.TempDir()
	snap := rawTokens{InputTokens: 1000, OutputTokens: 200, TotalTokens: 1200}
	lines := []string{
		tokenCount("2026-05-12T08:01:00.000Z", snap, nil),
		tokenCount("2026-05-12T08:02:00.000Z", snap, nil), // repeat: dropped
		tokenCount("2026-05-12T08:03:00.000Z", rawTokens{InputTokens: 1600, OutputTokens: 250, TotalTokens: 1850}, nil),
	}
	write(t, dir, "sessions/s.jsonl", join(lines))
	entries := mustParse(t, dir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if got := entries[1].InputTokens; got != 600 {
		t.Errorf("delta input = %d, want 600", got)
	}
}

func TestParseCachedInputClampedToInput(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-05-12T08:01:00.000Z","type":"event_msg","payload":{"type":"token_count",` +
		`"info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":400,"output_tokens":10,"total_tokens":110}}}}`
	write(t, dir, "sessions/s.jsonl", line)
	entries := mustParse(t, dir)
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].CacheReadTokens != 100 || entries[0].InputTokens != 0 {
		t.Errorf("cached not clamped: read=%d input=%d", entries[0].CacheReadTokens, entries[0].InputTokens)
	}
}

func TestParseServiceTierLifecycle(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		settingsLine("2026-07-22T00:00:00.000Z", `"service_tier":"priority"`),
		lastCount("2026-07-22T00:00:01.000Z", 10, 0, 1, 0),
		// Auto-review threads emit settings without a service_tier key: the
		// recorded tier must survive.
		settingsLine("2026-07-22T00:00:02.000Z", `"model":"codex-auto-review"`),
		lastCount("2026-07-22T00:00:03.000Z", 20, 0, 1, 0),
		// A recognized tier updates, an unrecognized one clears.
		settingsLine("2026-07-22T00:00:04.000Z", `"service_tier":"default"`),
		lastCount("2026-07-22T00:00:05.000Z", 30, 0, 1, 0),
		settingsLine("2026-07-22T00:00:06.000Z", `"service_tier":"flex"`),
		lastCount("2026-07-22T00:00:07.000Z", 40, 0, 1, 0),
	}
	write(t, dir, "sessions/s.jsonl", join(lines))
	entries := mustParse(t, dir)
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}
	wantFast := []bool{true, true, false, false}
	for i, w := range wantFast {
		if entries[i].SpeedFast != w {
			t.Errorf("entry %d SpeedFast = %v, want %v", i, entries[i].SpeedFast, w)
		}
	}
}

func TestParseHeadlessExecRecords(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"type":"turn.completed","timestamp":"2026-01-02T03:04:05.000Z","model":"gpt-5.2-codex",` +
			`"usage":{"input_tokens":120,"cached_input_tokens":20,"output_tokens":30,"total_tokens":150}}`,
		// prompt_tokens/cached_tokens/completion_tokens spelling inside data;
		// total derived as prompt+completion when recorded zero/absent.
		`{"type":"result","data":{"timestamp":"2026-01-02T03:05:05.000Z","model_name":"gpt-5.2-codex",` +
			`"usage":{"prompt_tokens":50,"cached_tokens":5,"completion_tokens":12}}}`,
		// Zero recorded total derives input+output without double-counting
		// reasoning (a subset of output).
		`{"type":"turn.completed","timestamp":"2026-01-02T03:06:05.000Z","model":"gpt-5.2-codex",` +
			`"usage":{"input_tokens":9,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":0}}`,
	}
	write(t, dir, "run.jsonl", join(lines))
	entries := mustParse(t, dir)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	if entries[0].InputTokens != 100 || entries[0].CacheReadTokens != 20 || entries[0].OutputTokens != 30 {
		t.Errorf("exec entry 0 wrong: %+v", entries[0])
	}
	if entries[1].InputTokens != 45 || entries[1].CacheReadTokens != 5 || entries[1].OutputTokens != 12 {
		t.Errorf("exec entry 1 wrong: in=%d read=%d out=%d", entries[1].InputTokens, entries[1].CacheReadTokens, entries[1].OutputTokens)
	}
	if entries[2].OutputTokens != 5 { // 4 output + 1 reasoning
		t.Errorf("derived-total reasoning fold wrong: %+v", entries[2])
	}
	for i, e := range entries {
		if e.Model != "gpt-5.2-codex" {
			t.Errorf("entry %d model = %q", i, e.Model)
		}
	}
}

// A session file whose token_count carries only total_token_usage with a lone
// grand total maps to ExtraTotalTokens (SPEC §3.2).
func TestParseTotalOnlyBecomesExtraTotalAfterSplitTurns(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		tokenCount("2026-05-12T08:01:00.000Z", rawTokens{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}, nil),
		`{"timestamp":"2026-05-12T08:02:00.000Z","type":"event_msg","payload":{"type":"token_count",` +
			`"info":{"total_token_usage":{"input_tokens":100,"output_tokens":50,"total_tokens":927}}}}`,
	}
	write(t, dir, "sessions/s.jsonl", join(lines))
	entries := mustParse(t, dir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[1].ExtraTotalTokens != 777 {
		t.Errorf("extra = %d, want 777 (delta total)", entries[1].ExtraTotalTokens)
	}
}

func TestDiscoverActiveCopyWinsOverArchivedDuplicate(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sessions/dup.jsonl", "{}\n")
	write(t, dir, "archived_sessions/dup.jsonl", "{}\n")
	write(t, dir, "archived_sessions/archive-only.jsonl", "{}\n")
	t.Setenv("CODEX_HOME", dir)
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2 (archived dup dropped): %v", len(files), files)
	}
	if !strings.Contains(files[0], filepath.Join("sessions", "dup.jsonl")) &&
		!strings.Contains(files[1], filepath.Join("sessions", "dup.jsonl")) {
		t.Errorf("active copy missing from results: %v", files)
	}
}

func TestNormalizeAlternateSpellingsAndDerivedTotal(t *testing.T) {
	cases := []struct {
		name string
		raw  rawTokens
		want rawTokens
	}{
		{
			name: "openai spellings",
			raw:  rawTokens{PromptTokens: 50, CachedTokens: 5, CompletionTok: 12, ReasoningAlt: 4},
			want: rawTokens{InputTokens: 50, CachedInputTokens: 5, OutputTokens: 12, ReasoningOutTokens: 4, TotalTokens: 62},
		},
		{
			name: "explicit zero total derives in+out",
			raw:  rawTokens{InputTokens: 9, OutputTokens: 4, ReasoningOutTokens: 1, TotalTokens: 0},
			want: rawTokens{InputTokens: 9, OutputTokens: 4, ReasoningOutTokens: 1, TotalTokens: 13},
		},
		{
			name: "recorded total kept",
			raw:  rawTokens{InputTokens: 100, OutputTokens: 50, ReasoningOutTokens: 20, TotalTokens: 151},
			want: rawTokens{InputTokens: 100, OutputTokens: 50, ReasoningOutTokens: 20, TotalTokens: 151},
		},
		{
			name: "cache_read_input_tokens alias",
			raw:  rawTokens{InputTokens: 10, CacheReadAlt: 3, OutputTokens: 2},
			want: rawTokens{InputTokens: 10, CachedInputTokens: 3, OutputTokens: 2, TotalTokens: 12},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.raw.normalize(); got != tc.want {
				t.Errorf("normalize() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func join(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out + "\n"
}

func mustParse(t *testing.T, dir string) []model.Entry {
	t.Helper()
	files, err := usageFilesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	var entries []model.Entry
	for _, f := range files {
		es, err := parse(f)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, es...)
	}
	return entries
}
