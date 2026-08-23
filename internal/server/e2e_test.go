package server

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "xusage/internal/sources/all" // wire the real claude/codex adapters

	"xusage/internal/engine"
	"xusage/internal/fixtures"
	"xusage/internal/model"
	"xusage/internal/pricing"
)

// The corpus is anchored to 2025-06-09/10; all aggregate assertions are
// phrased against those fixed days. Wall-clock-dependent endpoints (/api/live,
// active-block flags) use expectations that hold for any "now" after June 2025.

// e2eCorpus writes a small, fully hand-accounted Claude-shaped corpus into
// dir/claude/projects and returns the file path plus the expected entry count
// after dedup. Every line is emitted by fixtures.ClaudeLine so the bytes match
// real transcript shape (millisecond RFC3339 timestamps, message envelope).
//
// Layout per file:
//
//	e2e-a/sess-a1.jsonl  2 entries + 1 exact duplicate (same id+requestId)
//	e2e-a/sess-a2.jsonl  1 sidechain replay of sess-a1's first message
//	                     (dropped against the known main-chain request)
//	                     + 1 resumed-session copy of that message under a
//	                     fresh request ID + 1 entry
//	e2e-b/sess-b1.jsonl  1 malformed line, 1 <synthetic> line, 1 cwd:null line,
//	                     1 no-usage line, then 2 valid entries
//	e2e-b/sess-b2.jsonl  1 entry with a structured cache_creation 5m/1h split
//
// Raw usage lines: 3 + 3 + 6 + 1 = 13; after dedup: 7 entries.
func e2eCorpus(t *testing.T, dir string) (claudeDir string, wantEntries int) {
	t.Helper()
	claudeDir = filepath.Join(dir, "claude")

	write := func(rel string, lines ...string) {
		t.Helper()
		path := filepath.Join(claudeDir, "projects", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Day anchors stay explicit so the arithmetic is checkable at a glance:
	// day1 traffic lands on 2025-06-09, day0 traffic on 2025-06-10.
	day1 := time.Date(2025, 6, 9, 10, 0, 0, 0, time.UTC)
	day0 := time.Date(2025, 6, 10, 9, 0, 0, 0, time.UTC)
	line := func(id, req, sess string, ts time.Time, in, out, cc, cr uint64, cost *float64) string {
		return fixtures.ClaudeLine(fixtures.ClaudeLineOpts{
			Timestamp: ts, Version: "1.0.14",
			ID: id, RequestID: req, SessionID: sess,
			Model: "claude-sonnet-4-20250514",
			Input: in, Output: out, Cache5m: cc, CacheRead: cr,
			CostUSD: cost,
		})
	}
	// Reported costs only where noted; the rest must be priced from tokens.
	costA := fixtures.MustCost(0.05)

	// sess-a1: two valid entries; the second is replayed verbatim (same id +
	// request id) as a third line, so 3 raw lines collapse to 2 entries.
	write(filepath.Join("e2e-a", "sess-a1.jsonl"),
		line("msg_a1_0", "req_a1_0", "sess-a1", day1, 1000, 500, 200, 3000, nil),
		line("msg_a1_1", "req_a1_1", "sess-a1", day1.Add(3*time.Minute), 2000, 800, 0, 0, costA),
		line("msg_a1_1", "req_a1_1", "sess-a1", day1.Add(3*time.Minute), 2000, 800, 0, 0, costA),
	)

	// sess-a2: a sidechain replay of sess-a1's first message (fresh request id,
	// heavier cache read) — dropped because that request is already known from
	// sess-a1 — then a resumed-session copy of the same message under a fresh
	// request ID (kept: distinct request), then one more valid entry.
	write(filepath.Join("e2e-a", "sess-a2.jsonl"),
		fixtures.ClaudeLine(fixtures.ClaudeLineOpts{
			Timestamp: day1.Add(time.Hour), Version: "1.0.14",
			ID: "msg_a1_0", RequestID: "req-sidechain", SessionID: "sess-a2",
			Input: 900, Output: 120, CacheRead: 50_000, Sidechain: true,
		}),
		line("msg_a1_0", "req_a1_0-resumed", "sess-a2", day1.Add(time.Hour), 1000, 500, 200, 3000, nil),
		line("msg_a2_0", "req_a2_0", "sess-a2", day1.Add(time.Hour+5*time.Minute), 400, 100, 0, 0, nil),
	)

	// sess-b1: four noise lines the strict parser must reject, then two valid
	// entries. The cwd:null line is spelled by hand because ClaudeLineOpts can
	// only null sessionId/version/requestId/id/model/costUSD.
	write(filepath.Join("e2e-b", "sess-b1.jsonl"),
		fixtures.ClaudeLine(fixtures.ClaudeLineOpts{Malformed: true}),
		fixtures.ClaudeLine(fixtures.ClaudeLineOpts{
			Timestamp: day0, Synthetic: true,
			ID: "msg_syn", RequestID: "req_syn", SessionID: "sess-b1",
		}),
		`{"cwd":null,"sessionId":"sess-b1","version":"1.0.14","requestId":"req_null",`+
			`"timestamp":"`+day0.Format("2006-01-02T15:04:05.000Z")+`","message":{"id":"msg_null",`+
			`"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":10}}},`+
			`"content":"ok","isApiErrorMessage":false}`+"\n",
		fixtures.ClaudeLine(fixtures.ClaudeLineOpts{
			Timestamp: day0, NoUsage: true,
			ID: "msg_nousage", SessionID: "sess-b1",
		}),
		line("msg_b1_0", "req_b1_0", "sess-b1", day0, 1500, 300, 100, 0, nil),
		line("msg_b1_1", "req_b1_1", "sess-b1", day0.Add(2*time.Minute), 600, 200, 0, 500, nil),
	)

	// sess-b2: one entry with the structured cache_creation 5m/1h split.
	write(filepath.Join("e2e-b", "sess-b2.jsonl"),
		fixtures.ClaudeLine(fixtures.ClaudeLineOpts{
			Timestamp: day0.Add(30 * time.Minute), Version: "1.0.14",
			ID: "msg_b2_0", RequestID: "req_b2_0", SessionID: "sess-b2",
			Input: 700, Output: 250, Cache5m: 300, Cache1h: 900, CacheRead: 1200,
		}),
	)
	return claudeDir, 7
}

// loadE2ESnapshot points the real claude adapter at the corpus (every other
// adapter's override is pinned empty so no real user data can leak in) and
// runs the full engine.Load pipeline: discover → parse → price → dedup.
func loadE2ESnapshot(t *testing.T) *engine.Snapshot {
	t.Helper()
	dir := t.TempDir()
	claudeDir, want := e2eCorpus(t, dir)

	// Hermeticity: pin HOME/USERPROFILE at the empty temp dir and clear every
	// adapter's override env var, then point CLAUDE_CONFIG_DIR at the corpus.
	// Only the claude source may discover files.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, env := range []string{
		"PI_AGENT_DIR", "GOOSE_PATH_ROOT", "OPENCLAW_DIR", "KILO_DATA_DIR",
		"KIMI_DATA_DIR", "QWEN_DATA_DIR", "GEMINI_DATA_DIR", "GROK_HOME",
		"COPILOT_OTEL_FILE_EXPORTER_PATH", "CODEX_HOME", "OPENCODE_DATA_DIR",
		"AMP_DATA_DIR", "AMP_DIR", "CODEBUFF_DB_PATH", "DROID_SESSIONS_DIR",
		"HERMES_HOME",
	} {
		t.Setenv(env, "")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	snap, err := engine.Load()
	if err != nil {
		t.Fatalf("engine.Load: %v", err)
	}
	if len(snap.Entries) != want {
		var got []string
		for _, e := range snap.Entries {
			got = append(got, fmt.Sprintf("%s@%s sess=%s", e.ID, e.Timestamp.Format(time.RFC3339), e.SessionID))
		}
		t.Fatalf("deduped snapshot has %d entries, want %d:\n%s",
			len(snap.Entries), want, strings.Join(got, "\n"))
	}
	for _, e := range snap.Entries {
		if e.Provider != "claude" {
			t.Errorf("entry %s provider = %q, want claude (foreign data leaked in?)", e.ID, e.Provider)
		}
	}
	return snap
}

// TestE2EPipeline proves discover → parse → price → dedup → aggregate → serve
// in one hermetic pass: the snapshot comes from the real claude adapter on a
// hand-accounted corpus, and every assertion below runs against the real HTTP
// handlers through server.NewHandler().
func TestE2EPipeline(t *testing.T) {
	snap := loadE2ESnapshot(t)
	engine.SetForTest(t, snap)
	h := NewHandler()

	do := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d; body=%s", method, path, rec.Code, rec.Body.String())
		}
		return rec
	}
	abs := func(f float64) float64 { return math.Abs(f) }

	// --- discover: /api/meta reports claude detected with 4 files.
	meta := decode[struct {
		Providers []struct {
			Name      string `json:"name"`
			Detected  bool   `json:"detected"`
			FileCount int    `json:"fileCount"`
		} `json:"providers"`
	}](t, do(http.MethodGet, "/api/meta").Body)
	var claudeStatus *struct {
		Name      string `json:"name"`
		Detected  bool   `json:"detected"`
		FileCount int    `json:"fileCount"`
	}
	for i := range meta.Providers {
		if meta.Providers[i].Name == "claude" {
			claudeStatus = &meta.Providers[i]
		} else if meta.Providers[i].Detected {
			t.Errorf("provider %q detected on a claude-only corpus", meta.Providers[i].Name)
		}
	}
	if claudeStatus == nil {
		t.Fatal("meta.providers missing claude")
	}
	if !claudeStatus.Detected || claudeStatus.FileCount != 4 {
		t.Errorf("claude status = %+v, want detected with 4 files", *claudeStatus)
	}

	// --- dedup + pricing: summary totals are the hand-computed corpus values.
	// Expected tokens (6 surviving entries):
	//	in:  1000+2000 +1000+400 +1500+600 +700            = 7200
	//	out:  500+800  + 500+100 + 300+200 + 250           = 2650
	//	cc:   200+0    + 200+0   + 100+0   + 300+900(1h)   = 1700
	//	cr:  3000+0    +3000+0   + 0+500   + 1200           = 7700
	// Dropped in dedup: the verbatim duplicate of msg_a1_1 and the sidechain
	// replay of msg_a1_0 (900/120/50000). The resumed-session copy of msg_a1_0
	// under req_a1_0-resumed is a distinct request and must survive.
	want := engine.Summary{
		InputTokens: 7200, OutputTokens: 2650, CacheCreationTokens: 1700,
		CacheReadTokens: 7700,
	}
	want.TotalTokens = 7200 + 2650 + 1700 + 7700
	want.Messages, want.Sessions, want.Days, want.ModelCount = 7, 4, 2, 1
	want.FirstDate, want.LastDate = "2025-06-09", "2025-06-10"
	// Auto-mode cost = the one reported costUSD (0.05) plus the computed cost
	// of the six priced entries; recomputed below from the live pricing table
	// rather than hard-coded, so the snapshot can be refreshed independently.
	for i := range snap.Entries {
		e := &snap.Entries[i]
		if e.ReportedCost == nil {
			want.Cost += e.CostCalc
		} else {
			want.Cost += *e.ReportedCost
		}
	}
	want.ByProvider = []engine.ProviderBreakdown{{
		Provider: "claude", Cost: want.Cost, TotalTok: want.TotalTokens,
		Messages: 7, Sessions: 4,
	}}
	sum := decode[engine.Summary](t, do(http.MethodGet, "/api/summary").Body)
	if !summaryEqual(sum, want) {
		t.Errorf("summary = %+v\n     want %+v", sum, want)
	}
	if len(sum.ByProvider) != 1 || sum.ByProvider[0].Provider != "claude" {
		t.Errorf("byProvider = %+v, want a single claude row", sum.ByProvider)
	}

	// --- pricing applied: every entry must carry a positive computed cost.
	rates, known := pricing.Get().Find("claude-sonnet-4-20250514")
	if !known {
		t.Fatal("pricing snapshot does not know claude-sonnet-4-20250514; expected-cost math below is vacuous")
	}
	var wantCalc float64
	for i := range snap.Entries {
		e := &snap.Entries[i]
		wantCalc += rates.Cost(e.InputTokens, e.OutputTokens,
			e.CacheCreationTokens, e.CacheCreation1h, e.CacheReadTokens, e.SpeedFast)
		if e.CostCalc <= 0 {
			t.Errorf("entry %s CostCalc = %v, want > 0 (pricing not applied)", e.ID, e.CostCalc)
		}
	}

	// mode=calculate recomputes from tokens; mode=display keeps only the one
	// reported costUSD (0.05 on msg_a1_1).
	calc := decode[engine.Summary](t, do(http.MethodGet, "/api/summary?mode=calculate").Body)
	if abs(calc.Cost-wantCalc) > 1e-9 {
		t.Errorf("calculate-mode cost = %v, want %v", calc.Cost, wantCalc)
	}
	disp := decode[engine.Summary](t, do(http.MethodGet, "/api/summary?mode=display").Body)
	if abs(disp.Cost-0.05) > 1e-9 {
		t.Errorf("display-mode cost = %v, want 0.05 (the single reported costUSD)", disp.Cost)
	}

	// --- aggregate: /api/daily rows match engine output exactly, and the
	// per-day token buckets agree with the summary.
	type row = model.Row
	daily := decode[[]row](t, do(http.MethodGet, "/api/daily").Body)
	wantDaily := snap.Daily(&engine.Opts{})
	if len(daily) != len(wantDaily) {
		t.Fatalf("/api/daily returned %d rows, engine has %d", len(daily), len(wantDaily))
	}
	for i := range daily {
		if !rowsEqual(daily[i], wantDaily[i]) {
			t.Errorf("daily[%d] = %+v\n     want %+v", i, daily[i], wantDaily[i])
		}
	}
	var dayTokens uint64
	var dayCost float64
	for _, r := range daily {
		dayTokens += r.TotalTokens()
		dayCost += r.Cost
	}
	if dayTokens != want.TotalTokens {
		t.Errorf("daily token sum = %d, want %d", dayTokens, want.TotalTokens)
	}
	if abs(dayCost-sum.Cost) > 1e-9 {
		t.Errorf("daily cost sum = %v, want %v", dayCost, sum.Cost)
	}

	// --- weekly/monthly/hourly keys have the SPEC shapes.
	weekly := decode[[]row](t, do(http.MethodGet, "/api/weekly").Body)
	if len(weekly) == 0 {
		t.Fatal("weekly rows empty")
	}
	if monday := weekly[0].Key; !isMonday(monday) {
		t.Errorf("weekly key %q is not a Monday", monday)
	}
	monthly := decode[[]row](t, do(http.MethodGet, "/api/monthly").Body)
	if len(monthly) != 1 || monthly[0].Key != "2025-06" {
		t.Errorf("monthly = %+v, want one 2025-06 row", monthly)
	}
	hourly := decode[[]row](t, do(http.MethodGet, "/api/hourly").Body)
	if len(hourly) == 0 {
		t.Fatal("hourly rows empty")
	}
	for _, r := range hourly {
		if len(r.Key) != 13 || r.Key[10] != ' ' { // "2006-01-02 15"
			t.Errorf("hourly key %q does not match YYYY-MM-DD HH", r.Key)
		}
	}

	// --- tz: day keys are computed in the requested zone. Corpus times sit at
	// 09:00–11:05 UTC, so UTC yields 2025-06-09 + 2025-06-10; Pacific/Kiritimati
	// (+14) pushes even 09:00Z onto June 10, collapsing everything to one key.
	utc := decode[[]row](t, do(http.MethodGet, "/api/daily?tz=UTC").Body)
	if len(utc) != 2 || utc[0].Key != "2025-06-09" || utc[1].Key != "2025-06-10" {
		t.Errorf("tz=UTC daily = %v, want [2025-06-09 2025-06-10]", keys(utc))
	}
	nz := decode[[]row](t, do(http.MethodGet, "/api/daily?tz=Pacific/Kiritimati").Body)
	if len(nz) != 1 || nz[0].Key != "2025-06-10" {
		t.Errorf("tz=Pacific/Kiritimati daily = %v, want [2025-06-10]", keys(nz))
	}

	// --- sessions/projects/models carry the corpus structure.
	sessions := decode[[]row](t, do(http.MethodGet, "/api/sessions").Body)
	if len(sessions) != 4 {
		t.Errorf("sessions = %d rows, want 4", len(sessions))
	}
	if sessions[0].Key != "sess-b2" { // most recent activity first
		t.Errorf("newest session = %q, want sess-b2", sessions[0].Key)
	}
	limited := decode[[]row](t, do(http.MethodGet, "/api/sessions?limit=2").Body)
	if len(limited) != 2 {
		t.Errorf("sessions?limit=2 returned %d rows, want 2", len(limited))
	}
	projects := decode[[]row](t, do(http.MethodGet, "/api/projects").Body)
	if len(projects) != 2 || projects[0].Key != "e2e-a" || projects[1].Key != "e2e-b" {
		t.Errorf("projects = %+v, want e2e-a then e2e-b", projects)
	}
	models := decode[[]row](t, do(http.MethodGet, "/api/models").Body)
	if len(models) != 1 || models[0].Key != "claude-sonnet-4-20250514" {
		t.Errorf("models = %+v, want one claude-sonnet row", models)
	}

	// --- filters: project+since narrow the summary to e2e-a's day-1 traffic:
	// 4 entries across 2 sessions, 12700 tokens.
	filtered := decode[engine.Summary](t,
		do(http.MethodGet, "/api/summary?project=e2e-a&since=2025-06-09&until=2025-06-09").Body)
	if filtered.Sessions != 2 || filtered.TotalTokens != 12700 {
		t.Errorf("filtered summary = %+v, want 2 sessions / 12700 tokens", filtered)
	}

	// --- blocks: day-1 traffic (10:00–11:05 UTC) forms one block floored to
	// 10:00; the 21h silence until June-10 09:00 exceeds the 5h window, so
	// identifyBlocks inserts an explicit zero-token gap block between them and
	// opens a second real block at 09:00 (floor of the day-0 traffic). Every
	// entry must land in a non-gap block; gap blocks carry no tokens.
	blocks := decode[[]model.Block](t, do(http.MethodGet, "/api/blocks").Body)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d (%v), want 3 (block, gap, block)", len(blocks), blockStarts(blocks))
	}
	// handleBlocks returns latest first: [day-0 block, gap, day-1 block].
	if got := blocks[0].StartTime.UTC(); !got.Equal(time.Date(2025, 6, 10, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("latest block starts %v, want 2025-06-10 09:00 UTC", got)
	}
	if got := blocks[2].StartTime.UTC(); !got.Equal(time.Date(2025, 6, 9, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("oldest block starts %v, want 2025-06-09 10:00 UTC (floor to hour)", got)
	}
	if !blocks[1].IsGap {
		t.Errorf("middle block not flagged as gap: %+v", blocks[1])
	}
	gaps := 0
	for _, b := range blocks {
		if b.IsGap {
			gaps++
			if b.TotalTokens != 0 || b.InputTokens+b.OutputTokens+b.CacheCreationTokens+
				b.CacheReadTokens != 0 {
				t.Errorf("gap block %s carries tokens: %+v", b.ID, b)
			}
			continue
		}
		if b.IsActive {
			t.Errorf("block %s flagged active although it ended before now", b.ID)
		}
		if b.TotalTokens == 0 {
			t.Errorf("block %s carries no tokens", b.ID)
		}
	}
	if gaps != 1 {
		t.Errorf("gap block count = %d, want 1", gaps)
	}
	var blockTokens uint64
	for _, b := range blocks {
		blockTokens += b.TotalTokens
	}
	if blockTokens != want.TotalTokens {
		t.Errorf("block token sum = %d, want %d (every entry must land in a block)",
			blockTokens, want.TotalTokens)
	}

	// --- live: /api/live counts the real wall-clock day. The corpus is fixed
	// in 2025-06, so on any live run today's counters must be 0 and no block
	// can be active; updatedAt stays a fresh RFC3339 stamp.
	live := decode[struct {
		UpdatedAt string `json:"updatedAt"`
		Today     struct {
			Cost     float64 `json:"cost"`
			Tokens   uint64  `json:"tokens"`
			Messages int64   `json:"messages"`
		} `json:"today"`
		ActiveBlock *model.Block `json:"activeBlock"`
	}](t, do(http.MethodGet, "/api/live").Body)
	if live.Today.Tokens != 0 || live.Today.Cost != 0 || live.Today.Messages != 0 {
		t.Errorf("live today = %+v, want zeros (corpus is dated 2025-06)", live.Today)
	}
	if _, err := time.Parse(time.RFC3339Nano, live.UpdatedAt); err != nil {
		t.Errorf("live.updatedAt = %q: %v", live.UpdatedAt, err)
	}
	if live.ActiveBlock != nil {
		t.Errorf("live.activeBlock = %+v, want nil (all corpus blocks are past)", live.ActiveBlock)
	}

	// --- refresh keeps the same corpus through the real rescan path.
	refreshed := decode[struct {
		Entries int `json:"entries"`
	}](t, do(http.MethodPost, "/api/refresh").Body)
	if refreshed.Entries != 7 {
		t.Errorf("POST /api/refresh entries = %d, want 7", refreshed.Entries)
	}
}

// keys extracts row keys for readable failure output.
func keys(rows []model.Row) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].Key
	}
	return out
}

// blockStarts renders block start times for readable failure output.
func blockStarts(blocks []model.Block) []string {
	out := make([]string, len(blocks))
	for i := range blocks {
		out[i] = blocks[i].StartTime.UTC().Format("01-02 15:04") + "/" + blocks[i].ID
	}
	return out
}

// isMonday reports whether a YYYY-MM-DD key falls on a Monday.
func isMonday(key string) bool {
	d, err := time.Parse("2006-01-02", key)
	return err == nil && d.Weekday() == time.Monday
}

// rowsEqual compares two aggregate rows field by field (Row embeds slices,
// so it is not comparable with ==).
func rowsEqual(a, b model.Row) bool {
	if a.Key != b.Key || a.Label != b.Label ||
		a.InputTokens != b.InputTokens || a.OutputTokens != b.OutputTokens ||
		a.CacheCreationTokens != b.CacheCreationTokens || a.CacheReadTokens != b.CacheReadTokens ||
		a.ExtraTotalTokens != b.ExtraTotalTokens ||
		math.Abs(a.Cost-b.Cost) > 1e-9 ||
		a.Messages != b.Messages || a.Sessions != b.Sessions ||
		!a.FirstActivity.Equal(b.FirstActivity) || !a.LastActivity.Equal(b.LastActivity) ||
		len(a.Models) != len(b.Models) {
		return false
	}
	for i := range a.Models {
		if a.Models[i] != b.Models[i] {
			return false
		}
	}
	return true
}

// summaryEqual compares two Summary values field by field (Summary embeds a
// slice, so it is not comparable with ==).
func summaryEqual(a, b engine.Summary) bool {
	return a.InputTokens == b.InputTokens &&
		a.OutputTokens == b.OutputTokens &&
		a.CacheCreationTokens == b.CacheCreationTokens &&
		a.CacheReadTokens == b.CacheReadTokens &&
		a.ExtraTotalTokens == b.ExtraTotalTokens &&
		a.TotalTokens == b.TotalTokens &&
		math.Abs(a.Cost-b.Cost) <= 1e-9 &&
		a.Messages == b.Messages &&
		a.Sessions == b.Sessions &&
		a.Days == b.Days &&
		a.FirstDate == b.FirstDate &&
		a.LastDate == b.LastDate &&
		a.ModelCount == b.ModelCount &&
		len(a.ByProvider) == len(b.ByProvider)
}

// TestE2EServePathRunsRealBinaryShape boots the full NewHandler stack (API +
// embedded dashboard) against the fixture snapshot — the same composition
// Run serves — and checks the dashboard shell plus one asset.
func TestE2EServePathRunsRealBinaryShape(t *testing.T) {
	engine.SetForTest(t, loadE2ESnapshot(t))
	h := NewHandler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, frag := range []string{"<!DOCTYPE html>", "xusage", "/js/app.js", "/css/style.css"} {
		if !strings.Contains(body, frag) {
			t.Errorf("index.html missing %q", frag)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("index Content-Type = %q", ct)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/js/app.js", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("GET /js/app.js = %d (%d bytes)", rec.Code, rec.Body.Len())
	}

	// Unknown paths fall back to the SPA shell.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/deep/link", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
		t.Errorf("SPA fallback for /some/deep/link = %d", rec.Code)
	}
}
