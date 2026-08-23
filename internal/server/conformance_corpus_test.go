package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	_ "xusage/internal/sources/all" // link claude/codex adapters into this test binary

	"xusage/internal/engine"
	"xusage/internal/fixtures"
)

// registerCorpusSources repoints the real claude and codex adapters at a
// generated fixtures corpus via their env-var overrides, then loads it into
// the engine snapshot. Restoring an empty snapshot afterwards keeps other
// tests hermetic.
func registerCorpusSources(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	info, err := fixtures.WriteCorpus(dir, fixtures.CorpusOpts{
		Days:        3,
		Projects:    2,
		Sessions:    2,
		FilesPerDay: 2,
		WithErrors:  true,
	})
	if err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if info.Entries == 0 {
		t.Fatal("corpus generator produced no entries")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	snap, err := engine.Load()
	if err != nil {
		t.Fatalf("engine load: %v", err)
	}
	if len(snap.Entries) < 10 {
		t.Fatalf("fixture snapshot has %d entries, want a populated one", len(snap.Entries))
	}
	t.Cleanup(func() {
		empty, _ := engine.Load()
		engine.SetForTest(t, empty)
	})
}

// TestConformanceRealAdapterPipeline walks every analytics route against a
// snapshot built from real claude+codex adapter output on fixture data — the
// end-to-end path the unit-level fixture snapshot cannot cover.
func TestConformanceRealAdapterPipeline(t *testing.T) {
	registerCorpusSources(t)
	mux := testMux(t)

	for _, route := range []string{
		"/api/meta", "/api/summary", "/api/daily", "/api/weekly",
		"/api/monthly", "/api/hourly", "/api/models", "/api/sessions",
		"/api/projects", "/api/blocks", "/api/live",
	} {
		t.Run(route, func(t *testing.T) {
			rec := get(t, mux, route)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
			}
			var v any
			if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
		})
	}

	// Both fixture providers must surface as detected with real files.
	meta := decode[map[string]any](t, get(t, mux, "/api/meta").Body)
	detected := map[string]bool{}
	for _, p := range meta["providers"].([]any) {
		pm := p.(map[string]any)
		if pm["detected"].(bool) {
			detected[pm["name"].(string)] = true
		}
	}
	if !detected["claude"] || !detected["codex"] {
		t.Errorf("claude+codex should be detected, detected set: %v", detected)
	}

	// Summary totals must be nonzero and agree with the daily sum.
	sum := decode[map[string]any](t, get(t, mux, "/api/summary").Body)
	if sum["totalTokens"].(float64) <= 0 || sum["cost"].(float64) <= 0 {
		t.Fatalf("summary should carry nonzero totals on fixture data: %v", sum)
	}
	var dailyCost float64
	var dailyTokens float64
	for _, r := range decode[[]map[string]any](t, get(t, mux, "/api/daily").Body) {
		dailyCost += r["cost"].(float64)
		// Rows carry per-bucket counts only; total = sum of the buckets
		// (extraTotalTokens is omitted when zero, so it stays optional here).
		dailyTokens += r["inputTokens"].(float64) + r["outputTokens"].(float64) +
			r["cacheCreationTokens"].(float64) + r["cacheReadTokens"].(float64)
		if extra, ok := r["extraTotalTokens"].(float64); ok {
			dailyTokens += extra
		}
	}
	if abs(sum["cost"].(float64)-dailyCost) > 1e-6 {
		t.Errorf("summary cost %v != daily sum %v", sum["cost"], dailyCost)
	}
	if abs(sum["totalTokens"].(float64)-dailyTokens) > 1e-6 {
		t.Errorf("summary tokens %v != daily sum %v", sum["totalTokens"], dailyTokens)
	}

	// POST /api/refresh against the same env keeps both providers.
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/refresh = %d", rec.Code)
	}
	refreshed := decode[map[string]any](t, rec.Body)
	if refreshed["entries"].(float64) < 10 {
		t.Errorf("refresh saw %v entries, want the fixture corpus", refreshed["entries"])
	}
}
