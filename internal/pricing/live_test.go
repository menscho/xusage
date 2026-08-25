package pricing

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// silenceLogf swallows logf output for the test duration and returns a
// cleanup restoring the real logger.
func silenceLogf(t *testing.T) {
	t.Helper()
	orig := logf
	logf = func(string, ...any) {}
	t.Cleanup(func() { logf = orig })
}

// useTempDirs redirects the cache and override files into a throwaway tree and
// restores the embedded snapshot afterwards so other tests are unaffected.
func useTempDirs(t *testing.T) {
	t.Helper()
	cacheDir := t.TempDir()
	overrideDir := t.TempDir()
	co, oo := cacheDirOverride, overrideDirOverride
	cacheDirOverride, overrideDirOverride = cacheDir, overrideDir
	t.Cleanup(func() {
		cacheDirOverride, overrideDirOverride = co, oo
		SetTable(nil) // restore embedded snapshot
		resetAliasCache()
	})
}

// writeOverride writes the override file to the (redirected) override path.
func writeOverride(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(overridePath()), 0o755); err != nil {
		t.Fatalf("mkdir override dir: %v", err)
	}
	if err := os.WriteFile(overridePath(), []byte(content), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}

const liveOverrides = `{
  "override-model": { "input_cost_per_token": 5.0, "output_cost_per_token": 50.0 }
}`

func TestApplyAllAppliesOverridesOnTopOfEmbedded(t *testing.T) {
	useTempDirs(t)
	writeOverride(t, liveOverrides)

	// restore embedded snapshot; overrides must still apply.
	if err := applyAll(nil); err != nil {
		t.Fatalf("applyAll(nil): %v", err)
	}
	r, ok := Get().Find("override-model")
	if !ok {
		t.Fatal("override-model not found after applyAll(nil)")
	}
	if r.Input != 5.0 || r.Output != 50.0 {
		t.Fatalf("override-model rates = %+v, want input=5 output=50", r)
	}
	// embedded snapshot base intact
	if _, ok := Get().Find("gpt-5.2"); !ok {
		t.Fatal("embedded base missing after applyAll(nil)")
	}
}

func TestApplyAllLiveOverlayPlusOverrides(t *testing.T) {
	useTempDirs(t)
	writeOverride(t, liveOverrides)

	live := `{"live-model": {"input_cost_per_token": 7.0, "output_cost_per_token": 70.0}}`
	if err := applyAll([]byte(live)); err != nil {
		t.Fatalf("applyAll(live): %v", err)
	}
	// live overlay present
	r, ok := Get().Find("live-model")
	if !ok || r.Input != 7.0 || r.Output != 70.0 {
		t.Fatalf("live-model = %+v ok=%v, want input=7 output=70", r, ok)
	}
	// override present
	r, ok = Get().Find("override-model")
	if !ok || r.Input != 5.0 || r.Output != 50.0 {
		t.Fatalf("override-model = %+v ok=%v, want input=5 output=50", r, ok)
	}
	// embedded base present
	if _, ok := Get().Find("gpt-5.2"); !ok {
		t.Fatal("embedded base missing after live overlay")
	}
}

func TestApplyAllOverrideMalformedFallsBack(t *testing.T) {
	useTempDirs(t)
	writeOverride(t, "{not json")
	// A malformed overlay must not corrupt the table; embedded base still loads.
	if err := applyAll(nil); err != nil {
		t.Fatalf("applyAll(nil) with bad override: %v", err)
	}
	if _, ok := Get().Find("gpt-5.2"); !ok {
		t.Fatal("embedded base missing after malformed override")
	}
}

func TestCacheWriteThenRead(t *testing.T) {
	useTempDirs(t)
	payload := []byte(`{"x":{"input_cost_per_token":1.0,"output_cost_per_token":2.0}}`)
	if err := writeCache(payload); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	got, ok := readCache(time.Hour)
	if !ok {
		t.Fatal("readCache miss right after write")
	}
	if string(got) != string(payload) {
		t.Fatalf("readCache = %s, want %s", got, payload)
	}
}

func TestCacheExpiredIsMiss(t *testing.T) {
	useTempDirs(t)
	payload := []byte(`{"x":{"input_cost_per_token":1.0,"output_cost_per_token":2.0}}`)
	if err := writeCache(payload); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	// A negative TTL means everything is stale.
	if _, ok := readCache(-time.Second); ok {
		t.Fatal("expired cache reported as hit")
	}
}

func TestRefreshWithFetchesLiveWhenCacheStale(t *testing.T) {
	useTempDirs(t)
	silenceLogf(t)
	live := `{"live-model": {"input_cost_per_token": 9.0, "output_cost_per_token": 90.0}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(live))
	}))
	defer srv.Close()

	if err := refreshWith(0, srv.URL); err != nil {
		t.Fatalf("refreshWith: %v", err)
	}
	r, ok := Get().Find("live-model")
	if !ok || r.Input != 9.0 || r.Output != 90.0 {
		t.Fatalf("after live fetch live-model = %+v ok=%v, want 9/90", r, ok)
	}
	// cache written
	if _, ok := readCache(time.Hour); !ok {
		t.Fatal("live fetch did not populate cache")
	}
}

func TestRefreshWithUsesFreshCache(t *testing.T) {
	useTempDirs(t)
	silenceLogf(t)
	payload := []byte(`{"cache-model": {"input_cost_per_token": 11.0, "output_cost_per_token": 110.0}}`)
	if err := writeCache(payload); err != nil {
		t.Fatalf("writeCache: %v", err)
	}

	// Server that would fail the test if hit — fresh cache must short-circuit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("live fetch attempted despite fresh cache")
		http.Error(w, "should not fetch", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := refreshWith(time.Hour, srv.URL); err != nil {
		t.Fatalf("refreshWith: %v", err)
	}
	r, ok := Get().Find("cache-model")
	if !ok || r.Input != 11.0 || r.Output != 110.0 {
		t.Fatalf("cache-model = %+v ok=%v, want 11/110", r, ok)
	}
}

func TestRefreshWithFallsBackOnFetchError(t *testing.T) {
	useTempDirs(t)
	silenceLogf(t)
	// URL that will never connect.
	if err := refreshWith(0, "http://127.0.0.1:1/nope"); err != nil {
		t.Fatalf("refreshWith fallback returned error: %v", err)
	}
	// embedded snapshot restored
	if _, ok := Get().Find("gpt-5.2"); !ok {
		t.Fatal("embedded base missing after failed fetch")
	}
}

func TestRefreshWithFallsBackOnBadStatus(t *testing.T) {
	useTempDirs(t)
	silenceLogf(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := refreshWith(0, srv.URL); err != nil {
		t.Fatalf("refreshWith fallback returned error: %v", err)
	}
	if _, ok := Get().Find("gpt-5.2"); !ok {
		t.Fatal("embedded base missing after non-200 fetch")
	}
}

func TestRefreshWithFallsBackOnMalformedLive(t *testing.T) {
	useTempDirs(t)
	silenceLogf(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	if err := refreshWith(0, srv.URL); err != nil {
		t.Fatalf("refreshWith fallback returned error: %v", err)
	}
	if _, ok := Get().Find("gpt-5.2"); !ok {
		t.Fatal("embedded base missing after malformed live payload")
	}
}

func TestSetTableAppliesOverrides(t *testing.T) {
	useTempDirs(t)
	writeOverride(t, liveOverrides)
	if err := SetTable(nil); err != nil {
		t.Fatalf("SetTable(nil): %v", err)
	}
	r, ok := Get().Find("override-model")
	if !ok || r.Input != 5.0 || r.Output != 50.0 {
		t.Fatalf("SetTable(nil) override-model = %+v ok=%v, want 5/50", r, ok)
	}
}

func TestStartAutoRefreshIdempotentAndStops(t *testing.T) {
	useTempDirs(t)
	silenceLogf(t)

	// Prime a cache so the background goroutine's Refresh never touches network.
	payload := []byte(`{"auto-model": {"input_cost_per_token": 3.0, "output_cost_per_token": 30.0}}`)
	if err := writeCache(payload); err != nil {
		t.Fatalf("writeCache: %v", err)
	}

	StopAutoRefresh() // ensure clean slate
	StartAutoRefresh(time.Hour)
	StartAutoRefresh(time.Hour) // second call is a no-op (one goroutine)
	defer StopAutoRefresh()

	// The up-front refresh runs in a background goroutine; poll until the
	// cache-based overlay lands (no network is touched).
	deadline := time.Now().Add(time.Second)
	for {
		r, ok := Get().Find("auto-model")
		if ok && r.Input == 3.0 && r.Output == 30.0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto-model = %+v ok=%v, want 3/30", r, ok)
		}
		time.Sleep(time.Millisecond)
	}
	StopAutoRefresh()
}
