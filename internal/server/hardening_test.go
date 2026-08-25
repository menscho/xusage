package server

// Hardening tests: SPEC §8 wire-level guarantees the in-memory recorder hides —
// gzip framing, no-store on every API route, JSON error bodies, and snapshot
// swap safety under concurrent load. The engine package is linked in so
// POST /api/refresh exercises a real rescan without touching user data.

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "xusage/internal/sources/all"

	"xusage/internal/engine"
	"xusage/internal/fixtures"
	"xusage/internal/model"
)

// TestAPIRoutesNoStore pins SPEC §8's "no-store on /api/*": every API response,
// success or error, must disable client and intermediary caching.
func TestAPIRoutesNoStore(t *testing.T) {
	mux := testMux(t)
	engine.SetForTest(t, engine.NewSnapshotForTest(nil))
	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/api/meta"},
		{"GET", "/api/summary"},
		{"GET", "/api/daily?last=3&tz=UTC&mode=calculate"},
		{"GET", "/api/weekly"},
		{"GET", "/api/monthly"},
		{"GET", "/api/hourly"},
		{"GET", "/api/models"},
		{"GET", "/api/sessions?limit=5"},
		{"GET", "/api/projects"},
		{"GET", "/api/blocks?limit=2&active=true"},
		{"GET", "/api/live"},
		{"POST", "/api/refresh"},
		{"GET", "/api/nope"}, // unknown endpoint → JSON 404
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
				t.Errorf("Cache-Control = %q, want it to contain no-store", cc)
			}
		})
	}
}

// TestUnknownEndpointJSON404 pins the /api/ subtree contract: unknown endpoints
// get {"error":...} with status 404, never stdlib's plain-text response.
func TestUnknownEndpointJSON404(t *testing.T) {
	mux := testMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/definitely/not/here", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not a JSON object: %v", err)
	}
	if payload["error"] == "" {
		t.Errorf("body missing error field: %s", rec.Body.String())
	}
}

// TestMethodMatrix walks method × route across the whole API surface: supported
// pairs must answer 200 with valid JSON, everything else 405 with an error body.
func TestMethodMatrix(t *testing.T) {
	mux := testMux(t)
	engine.SetForTest(t, engine.NewSnapshotForTest(nil))

	getRoutes := []string{
		"/api/meta", "/api/summary", "/api/daily", "/api/weekly",
		"/api/monthly", "/api/hourly", "/api/models", "/api/sessions",
		"/api/projects", "/api/blocks", "/api/live",
	}
	for _, route := range getRoutes {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			req := httptest.NewRequest(m, route, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			want, name := http.StatusOK, m+" "+route
			if m != http.MethodGet {
				want, name = http.StatusMethodNotAllowed, "wrong-"+name
			}
			t.Run(name, func(t *testing.T) {
				if rec.Code != want {
					t.Fatalf("status = %d, want %d; body=%s", rec.Code, want, rec.Body.String())
				}
				var v any
				if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
					t.Fatalf("body is not JSON: %v", err)
				}
				if want == http.StatusMethodNotAllowed {
					obj, ok := v.(map[string]any)
					if !ok {
						t.Fatalf("405 body is not an object: %v", rec.Body.String())
					}
					if obj["error"] == "" {
						t.Errorf("405 body missing error field: %s", rec.Body.String())
					}
				}
			})
		}
	}

	// POST-only route: every other verb must be rejected too.
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/api/refresh", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/refresh status = %d, want 405", m, rec.Code)
		} else if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s /api/refresh 405 body missing error field: %s", m, rec.Body.String())
		}
	}
}

// TestBlocksRouteFiltersAndOrder checks limit + active semantics against a
// populated fixture snapshot: latest-first output, limit keeps the most recent
// blocks, and active=true yields only live ones.
func TestBlocksRouteFiltersAndOrder(t *testing.T) {
	now := time.Now()
	base := now.Add(-12 * 24 * time.Hour).Truncate(time.Hour)
	var entries []model.Entry
	for i := range 12 {
		ts := base.Add(time.Duration(i/2) * 24 * time.Hour).Add(17 * time.Hour)
		entries = append(entries, model.Entry{
			ID:           fmt.Sprintf("msg-%02d", i),
			Timestamp:    ts.UTC(),
			Model:        "claude-sonnet-4-5",
			InputTokens:  100,
			OutputTokens: 50,
			Messages:     1,
		})
	}
	engine.SetForTest(t, engine.NewSnapshotForTest(entries))
	mux := testMux(t)

	all := decode[[]map[string]any](t, get(t, mux, "/api/blocks").Body)
	if len(all) < 2 {
		t.Fatalf("fixture produced %d blocks, want several", len(all))
	}
	for i := 1; i < len(all); i++ {
		prev, cur := all[i-1]["startTime"].(string), all[i]["startTime"].(string)
		if prev < cur {
			t.Fatalf("blocks not latest-first: %v then %v", prev, cur)
		}
	}

	limited := decode[[]map[string]any](t, get(t, mux, "/api/blocks?limit=1").Body)
	if len(limited) != 1 || limited[0]["startTime"] != all[0]["startTime"] {
		t.Errorf("limit=1 kept wrong block: got %v, want newest %v", limited, all[0])
	}

	activeOnly := decode[[]map[string]any](t, get(t, mux, "/api/blocks?active=true").Body)
	if len(activeOnly) > len(all) {
		t.Fatal("active filter grew the result set")
	}
	for _, b := range activeOnly {
		if b["isActive"] != true {
			t.Errorf("active=true returned inactive block: %v", b)
		}
	}
}

// hardeningFixtureCorpus writes the shared fixtures corpus into t.TempDir() and
// repoints the real claude+codex adapters at it via their env overrides. It
// returns the corpus root (the directory POST /api/refresh rescans).
func hardeningFixtureCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	info, err := fixtures.WriteCorpus(dir, fixtures.CorpusOpts{
		Days:       2,
		Projects:   2,
		Sessions:   2,
		WithErrors: true,
	})
	if err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if info.Entries == 0 {
		t.Fatal("corpus generator produced no entries")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	return dir
}

// TestRefreshRouteRescansFiles proves POST /api/refresh re-runs discovery on
// the real adapters: a file written after the first load shows up in the next
// refresh's entry count.
func TestRefreshRouteRescansFiles(t *testing.T) {
	dir := hardeningFixtureCorpus(t)

	snap, err := engine.Load() // first scan: corpus as generated
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	firstCount := len(snap.Entries)

	// Grow the corpus: one extra Claude session file.
	claudeSess := filepath.Join(dir, "claude", "projects", "projHard", "sessHard.jsonl")
	line := `{"type":"assistant","sessionId":"s-hard","timestamp":"2026-01-01T10:00:00Z",` +
		`"message":{"id":"msg_hard_1","model":"claude-sonnet-4-5",` +
		`"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	if err := os.MkdirAll(filepath.Dir(claudeSess), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(claudeSess, []byte(line), 0o644); err != nil {
		t.Fatalf("grow corpus: %v", err)
	}

	rec := httptest.NewRecorder()
	testMux(t).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/refresh = %d; body=%s", rec.Code, rec.Body.String())
	}
	payload := decode[map[string]any](t, rec.Body)
	got := int(payload["entries"].(float64))
	if got <= firstCount {
		t.Errorf("refresh saw %d entries, want more than the initial %d (new file missed)", got, firstCount)
	}
	for _, key := range []string{"generatedAt", "providers"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("refresh payload missing %s", key)
		}
	}
}

// hermeticHome pins HOME/USERPROFILE at an empty dir and clears every adapter
// override env var, so a full Discover pass finds zero files. Without this, a
// developer's real ~/.claude would leak into serve-level tests.
func hermeticHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, env := range []string{
		"PI_AGENT_DIR", "GOOSE_PATH_ROOT", "OPENCLAW_DIR", "KILO_DATA_DIR",
		"KIMI_DATA_DIR", "QWEN_DATA_DIR", "GEMINI_DATA_DIR", "GROK_HOME",
		"COPILOT_OTEL_FILE_EXPORTER_PATH", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
		"OPENCODE_DATA_DIR", "AMP_DIR", "CODEBUFF_DB_PATH", "DROID_SESSIONS_DIR",
		"HERMES_HOME",
		// Newly-added source adapters (must be cleared so a developer's real
		// agent logs never leak into serve-level tests).
		"AUGMENT_DATA_DIR", "CLINE_HOME", "CURSOR_HOME", "DEVIN_DATA_DIR",
		"ROOCODE_DATA_DIR", "TRAE_DATA_DIR", "ZED_DATA_DIR", "XDG_CACHE_HOME",
	} {
		t.Setenv(env, "")
	}
}

// TestConcurrentReadersDuringSnapshotSwap hammers one handler set from many
// goroutines while snapshots are swapped underneath it — the exact window the
// atomic-pointer design must survive. A second worker pushes real adapter
// rescans through POST /api/refresh at the same time.
//
// Race-detection note: go test -race needs CGO/gcc (absent on this host), so
// interleaving is maximized instead — barrier start, high parallelism, Gosched
// yield points. Run with -count=1 -parallel 16 for best coverage.
func TestConcurrentReadersDuringSnapshotSwap(t *testing.T) {
	hermeticHome(t)
	ts := httptest.NewServer(muxWithWeb())
	defer ts.Close()

	engine.SetForTest(t, engine.NewSnapshotForTest(apiFixtureEntries()))

	const readersPerPath = 6
	paths := []string{
		"/api/meta", "/api/summary", "/api/daily", "/api/blocks",
		"/api/sessions?limit=10", "/api/models", "/api/live",
	}
	stop := make(chan struct{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, readersPerPath*len(paths))

	// One shared client with a generous per-host idle pool: 40+ tight-loop
	// readers each opening fresh connections would exhaust Windows ephemeral
	// ports and turn the test into a socket-churn benchmark.
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 64,
			MaxConnsPerHost:     128,
		},
	}
	for _, p := range paths {
		for i := 0; i < readersPerPath; i++ {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				<-start // release every reader at once
				n := 0
				for {
					select {
					case <-stop:
						if n == 0 {
							errCh <- fmt.Errorf("reader on %s made no requests", path)
						}
						return
					default:
					}
					resp, err := client.Get(ts.URL + path)
					if err != nil {
						errCh <- fmt.Errorf("GET %s: %v", path, err)
						return
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						errCh <- fmt.Errorf("GET %s status = %d", path, resp.StatusCode)
						return
					}
					n++
					runtime.Gosched()
				}
			}(p)
		}
	}

	go func() { // swap snapshots underneath the readers while they poll
		defer close(stop)
		<-start
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			snap := engine.Get()
			engine.SetForTest(nil, engine.NewSnapshotForTest(apiFixtureEntries()))
			_ = snap
			time.Sleep(time.Millisecond)
		}
	}()

	close(start) // release readers and the swap loop together
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestRunWithInjectedInterrupt proves the full serve path through Run's own
// listener: ready callback, live API responses, graceful shutdown on the
// interrupt source, and a released port afterwards.
func TestRunWithInjectedInterrupt(t *testing.T) {
	hermeticHome(t)
	interrupt := make(chan struct{})
	ready := make(chan string, 1)
	errCh := make(chan error, 1)
	// stop closes interrupt exactly once so runWithOptions always returns,
	// even on an early failure below — otherwise its poller goroutine leaks
	// into subsequent tests (StartPolling never stops a prior poller).
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(interrupt) }) }
	defer stop()

	go func() {
		errCh <- runWithOptions(Options{
			Addr:           "127.0.0.1:0", // ephemeral port: never collides
			PollInterval:   time.Second,
			RefreshOnStart: false,
			OnReady:        func(url string) { ready <- url },
		}, interrupt)
	}()

	var url string
	readyTimer := time.NewTimer(5 * time.Second)
	defer readyTimer.Stop()
	select {
	case url = <-ready:
	case <-readyTimer.C:
		t.Fatal("server never became ready")
	}

	meta, err := getBody(url+"/api/meta", "")
	if err != nil {
		t.Fatalf("GET /api/meta: %v", err)
	}
	var metaPayload map[string]any
	if err := json.Unmarshal([]byte(meta), &metaPayload); err != nil {
		t.Fatalf("meta body not JSON: %v (%q)", err, meta)
	}
	if metaPayload["version"] == "" {
		t.Errorf("meta missing version: %v", metaPayload)
	}

	stop()
	runTimer := time.NewTimer(5 * time.Second)
	defer runTimer.Stop()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on interrupt", err)
		}
	case <-runTimer.C:
		t.Fatal("Run did not shut down after interrupt")
	}

	addr := strings.TrimPrefix(url, "http://")
	if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		conn.Close()
		t.Errorf("port %s still accepting connections after shutdown", addr)
	}
}

// TestRunStopsPollerOnShutdown verifies the poller lifecycle behaviorally
// against a real fixtures corpus: a steady-state tick reparses nothing
// (incremental cache), a touched file is reparsed while running, and after
// shutdown further touches stay unparsed.
func TestRunStopsPollerOnShutdown(t *testing.T) {
	hermeticHome(t) // isolate from the developer's real agent logs
	dir := hardeningFixtureCorpus(t)

	// Find one corpus file to bump; the corpus always writes claude JSONL.
	var target string
	err := filepath.Walk(filepath.Join(dir, "claude"), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if target == "" && fi != nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			target = p
		}
		return nil
	})
	if err != nil || target == "" {
		t.Fatalf("no claude fixture file found: %v", err)
	}
	touch := func(offset time.Duration) {
		future := time.Now().Add(offset) // beat FS timestamp granularity
		if err := os.Chtimes(target, future, future); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	interrupt := make(chan struct{})
	ready := make(chan string, 1)
	errCh := make(chan error, 1)
	// stop closes interrupt exactly once so runWithOptions always returns,
	// even on an early failure below — otherwise its poller goroutine leaks
	// into subsequent tests (StartPolling never stops a prior poller).
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(interrupt) }) }
	defer stop()
	go func() {
		errCh <- runWithOptions(Options{
			Addr:           "127.0.0.1:0",
			PollInterval:   20 * time.Millisecond,
			RefreshOnStart: false,
			OnReady:        func(url string) { ready <- url },
		}, interrupt)
	}()
	readyTimer := time.NewTimer(5 * time.Second)
	defer readyTimer.Stop()
	select {
	case <-ready:
	case <-readyTimer.C:
		t.Fatal("server never became ready")
	}

	// Wait for parse activity to go quiet: the startup Refresh parses the
	// corpus, and NTFS may settle freshly written files' mtimes afterwards,
	// so the first ticks can legitimately reparse some files once. A broken
	// incremental cache would never go quiet; a healthy one stops within a
	// couple of poll intervals. Quiet = no parse for the final 150ms.
	waitForQuiet := func() (int64, bool) {
		deadline := time.Now().Add(2 * time.Second)
		last := engine.ParseCalls()
		lastChange := time.Now()
		for time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			if n := engine.ParseCalls(); n != last {
				last = n
				lastChange = time.Now()
			}
		}
		return last, time.Since(lastChange) >= 150*time.Millisecond
	}
	settled, ok := waitForQuiet()
	if !ok {
		t.Fatalf("parse calls never went quiet after startup (%d and climbing); cache reparsing every tick?", settled)
	}

	// Touch the file: the next tick must reparse exactly it.
	touch(10 * time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for engine.ParseCalls() <= settled && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := engine.ParseCalls(); got <= settled {
		t.Fatal("poller never reparsed the touched file")
	}

	stop()
	runTimer := time.NewTimer(5 * time.Second)
	defer runTimer.Stop()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-runTimer.C:
		t.Fatal("Run did not shut down after interrupt")
	}

	before := engine.ParseCalls()
	time.Sleep(20 * time.Millisecond) // let an in-flight tick land first
	before = engine.ParseCalls()
	touch(20 * time.Second)
	time.Sleep(150 * time.Millisecond) // several poll intervals' worth
	if after := engine.ParseCalls(); after != before {
		t.Errorf("parse calls advanced %d -> %d after shutdown; poller still running", before, after)
	}
}

// TestGzipFramingOverWire pins what httptest.ResponseRecorder cannot see: if a
// handler announces Content-Length for the plain asset but writes gzipped
// bytes, Go's http server truncates the response at that length. The gzipped
// asset must arrive complete and decompress byte-for-byte to the identity body.
func TestGzipFramingOverWire(t *testing.T) {
	ts := httptest.NewServer(muxWithWeb())
	defer ts.Close()

	identity, err := getBody(ts.URL+"/js/app.js", "")
	if err != nil {
		t.Skipf("app.js not served: %v", err)
	}
	body, err := getBody(ts.URL+"/js/app.js", "gzip")
	if err != nil {
		t.Fatalf("gzip request failed mid-body: %v", err)
	}
	zr, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip failed: %v", err)
	}
	if string(got) != string(identity) {
		t.Errorf("decompressed %d bytes, want %d (truncated or corrupted framing)", len(got), len(identity))
	}
}

// getBody fetches url over the real HTTP loopback with the given
// Accept-Encoding and returns the raw body bytes.
func getBody(url, acceptEncoding string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	tr := &http.Transport{DisableKeepAlives: true}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
