package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xusage/internal/engine"
	"xusage/internal/model"
)

// apiFixtureEntries builds five deterministic sessions across five days with
// one reported-cost entry, so limits, filters, and modes all have data to bite
// on. Costs are pre-resolved (NewSnapshotForTest does not re-price).
func apiFixtureEntries() []model.Entry {
	reported := 0.50
	ents := make([]model.Entry, 0, 5)
	for i := 0; i < 5; i++ {
		e := model.Entry{
			ID:          fmt.Sprintf("id-%d", i),
			RequestID:   fmt.Sprintf("req-%d", i),
			Timestamp:   timeUTC(2026, 6, 1+i, 12, 0),
			Model:       "claude-sonnet-4-20250514",
			Provider:    "api-test",
			Project:     "proj",
			SessionID:   fmt.Sprintf("sess-%d", i),
			InputTokens: 1000, OutputTokens: 500,
			CostAuto: 0.01, CostCalc: 0.01,
			Messages: 1,
		}
		if i == 0 { // exactly one entry carries a provider-reported cost
			e.ReportedCost = &reported
			e.CostDisplay = reported
			e.CostAuto = reported
		}
		ents = append(ents, e)
	}
	return ents
}

// apiBlockEntries builds entries forming three billing windows separated by
// gaps: a June 1st pair, a June 3rd pair (>5h later → gap block between), and
// two entries 20/30 minutes before now so the newest block is active with a
// positive elapsed span (burn rate needs a nonzero first→last spread).
func apiBlockEntries(now time.Time) []model.Entry {
	mk := func(id string, t time.Time, in uint64) model.Entry {
		return model.Entry{
			ID: id, Timestamp: t, Model: "m8x", Provider: "api-test",
			SessionID: "s-" + id, InputTokens: in, Messages: 1,
		}
	}
	ents := []model.Entry{
		mk("b1", timeUTC(2026, 6, 1, 10, 0), 100),
		mk("b2", timeUTC(2026, 6, 1, 12, 0), 100),
		mk("b3", timeUTC(2026, 6, 3, 9, 30), 100),
		mk("b4", timeUTC(2026, 6, 3, 11, 0), 100),
	}
	ents = append(ents,
		mk("b5", now.Add(-30*time.Minute), 3000),
		mk("b6", now.Add(-20*time.Minute), 3000))
	return ents
}

// TestConformanceSessionsLimitRespected asserts the limit param truncates the
// sessions list to the n most recent (BySession sorts LastActivity desc).
func TestConformanceSessionsLimitRespected(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, engine.NewSnapshotForTest(apiFixtureEntries()))

	all := decode[[]map[string]any](t, get(t, mux, "/api/sessions").Body)
	if len(all) != 5 {
		t.Fatalf("expected 5 sessions, got %d", len(all))
	}
	limited := decode[[]map[string]any](t, get(t, mux, "/api/sessions?limit=2").Body)
	if len(limited) != 2 {
		t.Fatalf("limit=2 kept %d sessions, want 2", len(limited))
	}
	if limited[0]["key"] != "sess-4" || limited[1]["key"] != "sess-3" {
		t.Errorf("limit=2 kept %v/%v, want the two most recent sess-4/sess-3",
			limited[0]["key"], limited[1]["key"])
	}

	// Garbage and negative limits fall back to the default, never error out.
	for _, q := range []string{"?limit=abc", "?limit=-3"} {
		rec := get(t, mux, "/api/sessions"+q)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", q, rec.Code, rec.Body.String())
		}
		rows := decode[[]map[string]any](t, rec.Body)
		if len(rows) != 5 {
			t.Errorf("%s: kept %d rows, want unfiltered 5", q, len(rows))
		}
	}
}

// TestConformanceSessionsDefaultLimitCapsAt200 pins the SPEC §8 default.
func TestConformanceSessionsDefaultLimitCapsAt200(t *testing.T) {
	mux := testMux(t)
	ents := make([]model.Entry, 0, 250)
	for i := 0; i < 250; i++ {
		ents = append(ents, model.Entry{
			ID: fmt.Sprintf("x%d", i), RequestID: fmt.Sprintf("r%d", i),
			Timestamp: timeUTC(2026, 6, 1, 12, 0),
			Model:     "m", Provider: "api-test",
			SessionID: fmt.Sprintf("s%03d", i), InputTokens: 1, Messages: 1,
		})
	}
	withSnapshot(t, engine.NewSnapshotForTest(ents))

	rows := decode[[]map[string]any](t, get(t, mux, "/api/sessions").Body)
	if len(rows) != 200 {
		t.Errorf("default limit returned %d rows, want 200", len(rows))
	}
}

// TestConformanceBlocksFiltersOnPopulatedData exercises limit + active against
// a snapshot whose newest block is genuinely live (entry 30min before now).
func TestConformanceBlocksFiltersOnPopulatedData(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, engine.NewSnapshotForTest(apiBlockEntries(time.Now())))

	all := decode[[]map[string]any](t, get(t, mux, "/api/blocks").Body)
	if len(all) != 5 { // jun1 block, gap, jun3 block, gap, live block
		t.Fatalf("expected 5 blocks, got %d", len(all))
	}
	// Latest-first ordering (SPEC §8).
	for i := 1; i < len(all); i++ {
		prev := all[i-1]["startTime"].(string)
		cur := all[i]["startTime"].(string)
		if prev < cur {
			t.Errorf("blocks not latest-first: %q before %q", prev, cur)
		}
	}

	active := decode[[]map[string]any](t, get(t, mux, "/api/blocks?active=true").Body)
	if len(active) != 1 {
		t.Fatalf("active=true returned %d blocks, want 1", len(active))
	}
	if active[0]["isActive"] != true {
		t.Errorf("filtered block isActive = %v, want true", active[0]["isActive"])
	}

	limited := decode[[]map[string]any](t, get(t, mux, "/api/blocks?limit=2").Body)
	if len(limited) != 2 {
		t.Fatalf("limit=2 returned %d blocks, want 2", len(limited))
	}
	if limited[0]["isActive"] != true {
		t.Errorf("most recent block should be the active one, got %v", limited[0])
	}
	if limited[0]["startTime"].(string) <= limited[1]["startTime"].(string) {
		t.Error("limit did not keep the most recent blocks")
	}
}

// TestConformanceBlocksGapHasZeroTokens asserts gap blocks surface through the
// API carrying no tokens and no cost — dead time must render honestly.
func TestConformanceBlocksGapHasZeroTokens(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, engine.NewSnapshotForTest(apiBlockEntries(time.Now())))

	blocks := decode[[]map[string]any](t, get(t, mux, "/api/blocks").Body)
	gaps := 0
	for _, b := range blocks {
		if b["isGap"] != true {
			continue
		}
		gaps++
		if b["totalTokens"].(float64) != 0 || b["cost"].(float64) != 0 {
			t.Errorf("gap block carries usage: %v", b)
		}
		start, err := time.Parse(time.RFC3339Nano, b["startTime"].(string))
		if err != nil {
			t.Fatalf("gap startTime unparsable: %v", err)
		}
		end, err := time.Parse(time.RFC3339Nano, b["endTime"].(string))
		if err != nil {
			t.Fatalf("gap endTime unparsable: %v", err)
		}
		if !start.Before(end) {
			t.Errorf("gap span inverted: %v ≥ %v", start, end)
		}
	}
	if gaps == 0 {
		t.Fatal("fixture spans should have produced gap blocks, got none")
	}
}

// TestConformanceTZShiftsDayKeys pins that a non-local timezone moves entries
// across day boundaries: both fixture entries fall on 2026-08-20 under UTC but
// land on the 21st under UTC+14 (Pacific/Kiritimati), independent of the
// host's own clock.
func TestConformanceTZShiftsDayKeys(t *testing.T) {
	mux := testMux(t)
	ents := []model.Entry{
		{ID: "t1", RequestID: "rt1", Timestamp: timeUTC(2026, 8, 20, 10, 0),
			Model: "m", Provider: "api-test", InputTokens: 10, Messages: 1},
		{ID: "t2", RequestID: "rt2", Timestamp: timeUTC(2026, 8, 20, 23, 30),
			Model: "m", Provider: "api-test", InputTokens: 10, Messages: 1},
	}
	withSnapshot(t, engine.NewSnapshotForTest(ents))

	utcRows := decode[[]map[string]any](t, get(t, mux, "/api/daily?tz=UTC").Body)
	if len(utcRows) != 1 || utcRows[0]["key"] != "2026-08-20" {
		t.Fatalf("tz=UTC keys = %v, want [2026-08-20]", keysOf(utcRows))
	}
	plus14 := decode[[]map[string]any](t, get(t, mux, "/api/daily?tz=Pacific%2FKiritimati").Body)
	if len(plus14) != 1 || plus14[0]["key"] != "2026-08-21" {
		t.Errorf("tz=Pacific/Kiritimati keys = %v, want [2026-08-21]", keysOf(plus14))
	}
	if math.Abs(utcRows[0]["cost"].(float64)-plus14[0]["cost"].(float64)) > 1e-9 {
		t.Error("total cost must be tz-independent")
	}
}

func keysOf(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r["key"].(string))
	}
	return out
}

// TestConformanceDailyModeChangesRowCosts pins mode semantics on the row
// endpoints (not just summary): display = reported only, calculate = computed
// only, auto blends both.
func TestConformanceDailyModeChangesRowCosts(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, engine.NewSnapshotForTest(apiFixtureEntries()))

	cost := func(query string) float64 {
		t.Helper()
		rows := decode[[]map[string]any](t, get(t, mux, "/api/daily"+query).Body)
		if len(rows) != 5 {
			t.Fatalf("%s: %d rows, want 5", query, len(rows))
		}
		var sum float64
		for _, r := range rows {
			sum += r["cost"].(float64)
		}
		return sum
	}
	display := cost("?mode=display&tz=UTC")
	calc := cost("?mode=calculate&tz=UTC")
	auto := cost("?tz=UTC")

	const eps = 1e-9
	if abs(display-0.50) > eps {
		t.Errorf("display = %v, want 0.50 (single reported cost)", display)
	}
	if abs(calc-0.05) > eps {
		t.Errorf("calculate = %v, want 0.05 (five computed entries)", calc)
	}
	if abs(auto-0.54) > eps {
		t.Errorf("auto = %v, want 0.54 (0.50 reported + 0.04 computed)", auto)
	}
}

// TestConformanceEmptyAnalyticsBodiesAreArrays asserts list endpoints serialize
// as [] rather than null when the snapshot is empty — client .map calls would
// crash on null.
func TestConformanceEmptyAnalyticsBodiesAreArrays(t *testing.T) {
	mux := testMux(t)
	engine.SetForTest(t, engine.NewSnapshotForTest(nil))

	for _, route := range []string{
		"/api/daily", "/api/weekly", "/api/monthly", "/api/hourly",
		"/api/models", "/api/sessions", "/api/projects", "/api/blocks",
	} {
		rec := get(t, mux, route)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", route, rec.Code)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
			t.Errorf("%s: body = %q, want []", route, body)
		}
	}
}

// TestConformanceErrorBodies pins the {"error":...} contract for wrong-method
// and unknown-endpoint requests.
func TestConformanceErrorBodies(t *testing.T) {
	mux := testMux(t)

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"post-only refresh rejects get", http.MethodGet, "/api/refresh", http.StatusMethodNotAllowed},
		{"refresh rejects put", http.MethodPut, "/api/refresh", http.StatusMethodNotAllowed},
		{"unknown api endpoint", http.MethodGet, "/api/nope", http.StatusNotFound},
		{"api subpath", http.MethodDelete, "/api/daily/sub", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, c.want, rec.Body.String())
			}
			body := decode[map[string]any](t, rec.Body)
			msg, ok := body["error"].(string)
			if !ok || msg == "" {
				t.Errorf("body missing error string: %v", body)
			}
		})
	}
}

// TestConformanceLenientQueryParams documents malformed-param handling: bad
// values are ignored (falling back to defaults) and yield 200s, per the
// "empty dataset → zero-valued 200s, never errors" spirit of SPEC §8.
func TestConformanceLenientQueryParams(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, engine.NewSnapshotForTest(apiFixtureEntries()))

	for _, q := range []string{
		"?since=not-a-date", "?until=2026-13-99", "?tz=Bogus/Zone",
		"?mode=hyperspeed", "?last=-1", "?project=",
	} {
		rec := get(t, mux, "/api/daily"+q)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d; body=%s", q, rec.Code, rec.Body.String())
		}
	}
}

// TestConformanceLiveWithActiveBlock asserts the full /api/live payload when a
// block is actually burning: activeBlock must carry usage, burn rate, and a
// projection alongside the always-present updatedAt/today fields.
func TestConformanceLiveWithActiveBlock(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, engine.NewSnapshotForTest(apiBlockEntries(time.Now())))

	rec := get(t, mux, "/api/live")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var live struct {
		UpdatedAt   string    `json:"updatedAt"`
		Today       *struct{} `json:"today"`
		ActiveBlock *struct {
			IsActive    bool           `json:"isActive"`
			IsGap       bool           `json:"isGap"`
			TotalTokens uint64         `json:"totalTokens"`
			BurnRate    map[string]any `json:"burnRate"`
			Projection  map[string]any `json:"projection"`
		} `json:"activeBlock"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil {
		t.Fatalf("decode live payload: %v", err)
	}
	if live.Today == nil {
		t.Error("today object missing")
	}
	if live.ActiveBlock == nil {
		t.Fatal("activeBlock null despite a block started 30 minutes ago")
	}
	if !live.ActiveBlock.IsActive || live.ActiveBlock.IsGap {
		t.Errorf("activeBlock flags wrong: %+v", live.ActiveBlock)
	}
	if live.ActiveBlock.TotalTokens == 0 {
		t.Error("activeBlock.totalTokens = 0, want the recent entry's tokens")
	}
	if live.ActiveBlock.BurnRate == nil || live.ActiveBlock.Projection == nil {
		t.Errorf("active block must carry burnRate+projection: %+v", live.ActiveBlock)
	}
}
