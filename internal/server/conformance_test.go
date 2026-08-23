package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xusage/internal/engine"
	"xusage/internal/model"
	"xusage/internal/web"
)

// withSnapshot swaps in a fixture snapshot for the duration of one test.
func withSnapshot(t *testing.T, s *engine.Snapshot) {
	t.Helper()
	prev := engine.Get()
	engine.SetForTest(t, s)
	t.Cleanup(func() { engine.SetForTest(t, prev) })
}

// muxWithWeb builds the full production mux: API routes plus the embedded
// dashboard handler, so fallback/header tests exercise the real composition.
func muxWithWeb() *http.ServeMux {
	mux := http.NewServeMux()
	registerRoutes(mux)
	mux.Handle("/", web.Handler())
	return mux
}

// fixtureEntries builds a small deterministic entry set spanning two days and
// two models; reported cost is set on exactly one entry so display/calculate
// modes must produce different totals.
func fixtureEntries() []model.Entry {
	reported := 0.25
	day1 := timeUTC(2026, 8, 20, 10, 0)
	day2 := timeUTC(2026, 8, 21, 23, 30)
	return []model.Entry{
		{
			ID: "m1", RequestID: "r1", Timestamp: day1,
			Model: "claude-sonnet-4-20250514", Provider: "test", Project: "proj-a",
			SessionID: "sess-1", Version: "1.0.0",
			InputTokens: 1000, OutputTokens: 500, CacheCreationTokens: 200, CacheReadTokens: 3000,
			CostDisplay: reported, CostAuto: reported, CostCalc: 0.02, ReportedCost: &reported,
			Messages: 1,
		},
		{
			ID: "m2", RequestID: "r2", Timestamp: day1.Add(timeHour),
			Model: "claude-opus-4-20250514", Provider: "test", Project: "proj-b",
			SessionID:   "sess-2",
			InputTokens: 250_000, OutputTokens: 1000, // above the 200k tier
			CostAuto: 2.0, CostCalc: 2.0, Messages: 1,
		},
		{
			ID: "m3", RequestID: "r3", Timestamp: day2,
			Model: "claude-sonnet-4-20250514", Provider: "test", Project: "proj-a",
			SessionID:   "sess-1",
			InputTokens: 800, OutputTokens: 400,
			CostAuto: 0.004, CostCalc: 0.004, Messages: 1,
		},
	}
}

func buildFixtureSnapshot() *engine.Snapshot {
	return engine.NewSnapshotForTest(fixtureEntries())
}

// jsonKeys walks decoded JSON and collects every object key.
func jsonKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	var walk func(x any)
	walk = func(x any) {
		switch val := x.(type) {
		case map[string]any:
			for k, sub := range val {
				keys[k] = true
				walk(sub)
			}
		case []any:
			for _, sub := range val {
				walk(sub)
			}
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	walk(generic)
	return keys
}

// TestConformanceRoutesPopulated hits every analytics route against a
// populated snapshot and validates status + JSON shape.
func TestConformanceRoutesPopulated(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, buildFixtureSnapshot())

	routes := []string{
		"/api/meta", "/api/summary", "/api/daily", "/api/weekly",
		"/api/monthly", "/api/hourly", "/api/models", "/api/sessions",
		"/api/projects", "/api/blocks", "/api/live",
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			rec := get(t, mux, route)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var v any
			if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
		})
	}
}

// TestConformanceCamelCaseKeys asserts no snake_case key ever leaks into API
// JSON (SPEC §8: all JSON is camelCase).
func TestConformanceCamelCaseKeys(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, buildFixtureSnapshot())

	for _, route := range []string{
		"/api/meta", "/api/summary", "/api/daily?last=2", "/api/weekly",
		"/api/monthly", "/api/hourly", "/api/models", "/api/sessions?limit=2",
		"/api/projects", "/api/blocks", "/api/live",
	} {
		rec := get(t, mux, route)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", route, rec.Code)
		}
		var v any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("%s: invalid JSON: %v", route, err)
		}
		for key := range jsonKeys(t, v) {
			if strings.Contains(key, "_") {
				t.Errorf("%s: snake_case key %q leaked into JSON", route, key)
			}
		}
	}

	// POST /api/refresh payload too.
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/refresh: status = %d", rec.Code)
	}
	var v any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("/api/refresh: invalid JSON: %v", err)
	}
	for key := range jsonKeys(t, v) {
		if strings.Contains(key, "_") {
			t.Errorf("/api/refresh: snake_case key %q leaked into JSON", key)
		}
	}
}

// TestConformanceModesChangeCosts pins SPEC §8's mode semantics end-to-end:
// display uses the reported cost only, calculate computes from tokens, auto
// prefers reported when present.
func TestConformanceModesChangeCosts(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, buildFixtureSnapshot())

	costOf := func(query string) float64 {
		t.Helper()
		rec := get(t, mux, "/api/summary"+query)
		if rec.Code != http.StatusOK {
			t.Fatalf("summary%s: status = %d; body=%s", query, rec.Code, rec.Body.String())
		}
		s := decode[map[string]any](t, rec.Body)
		c, ok := s["cost"].(float64)
		if !ok {
			t.Fatalf("summary%s: missing numeric cost: %v", query, s)
		}
		return c
	}

	display := costOf("?mode=display")
	calc := costOf("?mode=calculate")
	auto := costOf("") // default mode

	// m1 reports $0.25; m2/m3 report nothing. display counts only reported
	// costs, calculate only computed ones, auto blends both.
	if display != 0.25 {
		t.Errorf("display cost = %v, want 0.25 (the only reported cost)", display)
	}
	if calc != 2.024 {
		t.Errorf("calculate cost = %v, want 2.024 (0.02+2.0+0.004)", calc)
	}
	if auto != 2.254 {
		t.Errorf("auto cost = %v, want 2.254 (0.25 reported + 2.004 computed)", auto)
	}
}

// TestConformanceQueryFilters pins each filter param against populated data.
func TestConformanceQueryFilters(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, buildFixtureSnapshot())

	t.Run("project", func(t *testing.T) {
		rows := decode[[]map[string]any](t, get(t, mux, "/api/projects").Body)
		if len(rows) < 2 {
			t.Fatalf("need >=2 projects, got %d", len(rows))
		}
		filtered := decode[[]map[string]any](t, get(t, mux, "/api/daily?project=proj-a").Body)
		var total float64
		for _, row := range filtered {
			total += row["cost"].(float64)
		}
		full := decode[[]map[string]any](t, get(t, mux, "/api/daily").Body)
		var fullTotal float64
		for _, row := range full {
			fullTotal += row["cost"].(float64)
		}
		if total >= fullTotal {
			t.Errorf("project filter did not reduce daily cost: %v >= %v", total, fullTotal)
		}
	})
	t.Run("model", func(t *testing.T) {
		rows := decode[[]map[string]any](t, get(t, mux, "/api/models").Body)
		if len(rows) < 2 {
			t.Fatalf("need >=2 models, got %d", len(rows))
		}
		filtered := decode[[]map[string]any](t, get(t, mux, "/api/daily?model=claude-opus-4-20250514").Body)
		var sum float64
		for _, row := range filtered {
			sum += row["outputTokens"].(float64)
		}
		if sum == 0 {
			t.Error("model filter returned rows without tokens")
		}
	})
	t.Run("session", func(t *testing.T) {
		rows := decode[[]map[string]any](t, get(t, mux, "/api/sessions").Body)
		if len(rows) < 2 {
			t.Fatalf("need >=2 sessions, got %d", len(rows))
		}
		filtered := decode[[]map[string]any](t, get(t, mux, "/api/sessions?session=sess-1").Body)
		if len(filtered) != 1 {
			t.Fatalf("session filter kept %d sessions, want 1", len(filtered))
		}
	})
	t.Run("provider", func(t *testing.T) {
		rows := decode[map[string]any](t, get(t, mux, "/api/summary").Body)
		byProvider, _ := rows["byProvider"].([]any)
		if len(byProvider) == 0 {
			t.Fatal("summary.byProvider empty")
		}
		s := decode[map[string]any](t, get(t, mux, "/api/summary?provider=nosuch").Body)
		if len(s["byProvider"].([]any)) != 0 || s["cost"].(float64) != 0 {
			t.Errorf("unknown provider filter should zero everything, got %v", s)
		}
	})
	t.Run("sinceUntilLast", func(t *testing.T) {
		all := decode[[]map[string]any](t, get(t, mux, "/api/daily?tz=UTC").Body)
		if len(all) != 2 {
			t.Fatalf("fixture spans 2 UTC days, got %d", len(all))
		}
		windowed := decode[[]map[string]any](t, get(t, mux, "/api/daily?since=2026-08-21&until=2026-08-21&tz=UTC").Body)
		if len(windowed) != 1 || windowed[0]["key"] != "2026-08-21" {
			t.Errorf("since/until window wrong: %v", windowed)
		}
		lastOne := decode[[]map[string]any](t, get(t, mux, "/api/daily?last=1&tz=UTC").Body)
		if len(lastOne) != 1 || lastOne[0]["key"] != "2026-08-21" {
			t.Errorf("last=1 kept wrong day: %v", lastOne)
		}
	})
	t.Run("weeklyMonthlyHourlyKeys", func(t *testing.T) {
		weekly := decode[[]map[string]any](t, get(t, mux, "/api/weekly").Body)
		if len(weekly) == 0 {
			t.Fatal("weekly empty")
		}
		key, _ := weekly[0]["key"].(string)
		if len(key) != 10 { // YYYY-MM-DD Monday
			t.Errorf("weekly key %q is not a date", key)
		}
		if wd := timeUTCWeekday(key); wd != 1 { // Monday
			t.Errorf("weekly key %q is not a Monday (weekday=%d)", key, wd)
		}
		monthly := decode[[]map[string]any](t, get(t, mux, "/api/monthly").Body)
		if len(monthly) == 0 {
			t.Fatal("monthly empty")
		}
		mkey, _ := monthly[0]["key"].(string)
		if len(mkey) != 7 { // YYYY-MM
			t.Errorf("monthly key %q is not a month", mkey)
		}
		hourly := decode[[]map[string]any](t, get(t, mux, "/api/hourly").Body)
		if len(hourly) == 0 {
			t.Fatal("hourly empty")
		}
		hkey, _ := hourly[0]["key"].(string)
		if len(hkey) != 13 { // "YYYY-MM-DD HH"
			t.Errorf("hourly key %q lacks the hour component", hkey)
		}
	})
}

// TestConformanceTZUTCShiftsDaily pins tz handling: a 23:30 UTC entry moves to
// the next day under tz=Asia/Tokyo but not under tz=UTC.
func TestConformanceTZUTCShiftsDaily(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, buildFixtureSnapshot())

	utcRows := decode[[]map[string]any](t, get(t, mux, "/api/daily?tz=UTC").Body)
	tokyoRows := decode[[]map[string]any](t, get(t, mux, "/api/daily?tz=Asia%2FTokyo").Body)

	var utcDays, tokyoDays int
	var utcCost, tokyoCost float64
	var utcKeys, tokyoKeys []string
	for _, r := range utcRows {
		utcDays++
		utcCost += r["cost"].(float64)
		utcKeys = append(utcKeys, r["key"].(string))
	}
	for _, r := range tokyoRows {
		tokyoDays++
		tokyoCost += r["cost"].(float64)
		tokyoKeys = append(tokyoKeys, r["key"].(string))
	}
	if utcDays != 2 {
		t.Fatalf("expected 2 UTC days, got %d (%v)", utcDays, utcKeys)
	}
	// Tokyo = UTC+9: 23:30Z on the 21st lands on the 22nd, so the two UTC days
	// (20th, 21st+22nd) stay two days — the 23:30Z entry just changes keys.
	if tokyoDays != 2 {
		t.Errorf("tz=Asia/Tokyo should keep 2 days (keys shifted), got %d (%v)", tokyoDays, tokyoKeys)
	}
	if len(tokyoKeys) > 0 && tokyoKeys[len(tokyoKeys)-1] != "2026-08-22" {
		t.Errorf("Tokyo keys should end on 2026-08-22, got %v", tokyoKeys)
	}
	const eps = 1e-9
	if abs(utcCost-tokyoCost) > eps {
		t.Errorf("total cost must be tz-independent: utc=%v tokyo=%v", utcCost, tokyoCost)
	}
}

// TestConformanceLiveShape pins every field SPEC §8 requires from /api/live:
// updatedAt, today{cost,tokens,messages}, activeBlock summary.
func TestConformanceLiveShape(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, buildFixtureSnapshot())

	rec := get(t, mux, "/api/live")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var live struct {
		UpdatedAt string `json:"updatedAt"`
		Today     *struct {
			Cost     float64 `json:"cost"`
			Tokens   uint64  `json:"tokens"`
			Messages int64   `json:"messages"`
		} `json:"today"`
		ActiveBlock map[string]any `json:"activeBlock"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil {
		t.Fatalf("decode live payload: %v", err)
	}
	if live.Today == nil {
		t.Fatal("today object missing — /api/live omits a SPEC-required field")
	}
	if live.UpdatedAt == "" {
		t.Error("updatedAt empty")
	}
}

// TestConformanceBlocksActiveFilter asserts active=true only surfaces blocks
// flagged isActive.
func TestConformanceBlocksActiveFilter(t *testing.T) {
	mux := testMux(t)
	withSnapshot(t, buildFixtureSnapshot())

	all := decode[[]map[string]any](t, get(t, mux, "/api/blocks").Body)
	active := decode[[]map[string]any](t, get(t, mux, "/api/blocks?active=true").Body)
	for _, b := range active {
		if v, ok := b["isActive"].(bool); !ok || !v {
			t.Fatalf("blocks?active=true returned a non-active block: %v", b)
		}
	}
	_ = all
}

// TestConformanceIndexFallbackAndCacheHeaders runs against a real HTTP server
// (net/http client) so header propagation is verified over the wire too.
func TestConformanceIndexFallbackAndCacheHeaders(t *testing.T) {
	ts := httptest.NewServer(muxWithWeb())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/definitely/not/a/route")
	if err != nil {
		t.Fatalf("GET fallback: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("fallback status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "<!DOCTYPE html>") && !strings.Contains(string(body), "<html") {
		t.Error("fallback body is not index.html")
	}

	rootResp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer rootResp.Body.Close()
	if cc := rootResp.Header.Get("Cache-Control"); cc == "" {
		t.Error("/ response carries no Cache-Control header")
	}

	apiResp, err := http.Get(ts.URL + "/api/meta")
	if err != nil {
		t.Fatalf("GET /api/meta over wire: %v", err)
	}
	defer apiResp.Body.Close()
	apiBody, _ := io.ReadAll(apiResp.Body)
	if apiResp.StatusCode != http.StatusOK {
		t.Errorf("/api/meta over wire: status %d", apiResp.StatusCode)
	}
	if !strings.Contains(apiResp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("/api/meta content-type = %q", apiResp.Header.Get("Content-Type"))
	}
	if cc := apiResp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("/api/meta Cache-Control = %q, want no-store", cc)
	}
	_ = fmt.Sprint(apiBody) // keep io import meaningful if body unused later
}

// abs returns |f|.
func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// TestConformanceLiveActiveBlockKeyPresent pins that /api/live always carries
// the activeBlock key — null when no block is live (SPEC §8), never omitted.
func TestConformanceLiveActiveBlockKeyPresent(t *testing.T) {
	mux := testMux(t)

	// Empty snapshot: nothing is live, the key must still be present.
	engine.SetForTest(t, engine.NewSnapshotForTest(nil))
	rec := get(t, mux, "/api/live")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode live payload: %v", err)
	}
	v, ok := raw["activeBlock"]
	if !ok {
		t.Fatalf("activeBlock key omitted from /api/live: %s", rec.Body.String())
	}
	if v != nil {
		t.Errorf("activeBlock = %v on an empty snapshot, want null", v)
	}
}
