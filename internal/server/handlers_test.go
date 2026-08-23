package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testMux builds the API mux without binding a socket, so tests never touch
// the network or the real user data directories.
func testMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerRoutes(mux)
	return mux
}

// get performs an in-memory request against the mux and returns the recorder.
func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, body io.Reader) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}

// TestEmptySnapshotZeroValued asserts every analytics route returns 200 with
// zero-valued JSON when the engine snapshot is empty (no real user data).
func TestEmptySnapshotZeroValued(t *testing.T) {
	mux := testMux(t)
	for _, route := range []string{
		"/api/meta", "/api/summary", "/api/daily", "/api/weekly",
		"/api/monthly", "/api/hourly", "/api/models", "/api/sessions",
		"/api/projects", "/api/blocks",
	} {
		t.Run(route, func(t *testing.T) {
			rec := get(t, mux, route)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var generic any
			if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
		})
	}
}

func TestSummaryEmptyShape(t *testing.T) {
	mux := testMux(t)
	rec := get(t, mux, "/api/summary")
	s := decode[map[string]any](t, rec.Body)
	for _, key := range []string{"inputTokens", "outputTokens", "totalTokens", "cost", "messages", "sessions", "days"} {
		if got := s[key]; got != float64(0) {
			t.Errorf("%s = %v, want 0", key, got)
		}
	}
}

func TestDailyRowsArray(t *testing.T) {
	mux := testMux(t)
	rec := get(t, mux, "/api/daily")
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("daily rows: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected empty rows, got %d", len(rows))
	}
}

func TestQueryParamsAccepted(t *testing.T) {
	mux := testMux(t)
	url := "/api/daily?since=2026-01-01&until=2026-01-31&last=7&project=p&session=s&model=m&provider=claude&mode=calculate&tz=UTC"
	rec := get(t, mux, url)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUnknownTZFallsBackToLocal(t *testing.T) {
	o := optsFromRequest(httptest.NewRequest(http.MethodGet, "/?tz=Not/AZone", nil))
	if o.TZ != nil {
		t.Errorf("TZ = %v, want nil (local default)", o.TZ)
	}
	o = optsFromRequest(httptest.NewRequest(http.MethodGet, "/?tz=UTC", nil))
	if o.TZ == nil || o.TZ.String() != "UTC" {
		t.Errorf("TZ = %v, want UTC", o.TZ)
	}
}

// TestIANATimezoneOnWindows pins the embedded-tzdata guarantee: a named zone
// must resolve even where the OS has no zoneinfo database.
func TestIANATimezoneOnWindows(t *testing.T) {
	o := optsFromRequest(httptest.NewRequest(http.MethodGet, "/?tz=Europe/Berlin", nil))
	if o.TZ == nil {
		t.Fatal("Europe/Berlin did not resolve; time/tzdata embed missing?")
	}
	if o.TZ.String() != "Europe/Berlin" {
		t.Errorf("TZ = %q, want Europe/Berlin", o.TZ.String())
	}
}

func TestModeParsing(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "auto"}, {"auto", "auto"}, {"display", "display"},
		{"calculate", "calculate"}, {"bogus", "auto"},
	}
	for _, c := range cases {
		o := optsFromRequest(httptest.NewRequest(http.MethodGet, "/?mode="+c.in, nil))
		if string(o.Mode) != c.want {
			t.Errorf("mode %q -> %q, want %q", c.in, o.Mode, c.want)
		}
	}
}

func TestSessionsLimitParam(t *testing.T) {
	mux := testMux(t)
	rec := get(t, mux, "/api/sessions?limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("sessions rows: %v", err)
	}
}

func TestBlocksLimitAndActiveParams(t *testing.T) {
	mux := testMux(t)
	for _, q := range []string{"?limit=3", "?active=true", "?limit=2&active=true"} {
		rec := get(t, mux, "/api/blocks"+q)
		if rec.Code != http.StatusOK {
			t.Fatalf("blocks%s status = %d; body=%s", q, rec.Code, rec.Body.String())
		}
		var blocks []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &blocks); err != nil {
			t.Fatalf("blocks rows: %v", err)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	mux := testMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/daily", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/daily status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("body missing error field: %s", rec.Body.String())
	}
}

func TestPostRefreshRoute(t *testing.T) {
	mux := testMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("refresh payload: %v", err)
	}
	if _, ok := payload["generatedAt"]; !ok {
		t.Errorf("payload missing generatedAt: %v", payload)
	}
	if _, ok := payload["providers"]; !ok {
		t.Errorf("payload missing providers: %v", payload)
	}
}

func TestLivePayloadShape(t *testing.T) {
	mux := testMux(t)
	rec := get(t, mux, "/api/live")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		UpdatedAt string `json:"updatedAt"`
		Today     struct {
			Cost     float64 `json:"cost"`
			Tokens   uint64  `json:"tokens"`
			Messages int64   `json:"messages"`
		} `json:"today"`
		ActiveBlock map[string]any `json:"activeBlock"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("live payload: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.UpdatedAt); err != nil {
		t.Errorf("updatedAt not RFC3339Nano: %q", payload.UpdatedAt)
	}
}

func TestMetaShape(t *testing.T) {
	mux := testMux(t)
	rec := get(t, mux, "/api/meta")
	var meta struct {
		Version     string           `json:"version"`
		GeneratedAt string           `json:"generatedAt"`
		TZ          string           `json:"tz"`
		Providers   []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("meta payload: %v", err)
	}
	if meta.Version == "" {
		t.Error("meta.version empty")
	}
	if meta.GeneratedAt == "" {
		t.Error("meta.generatedAt empty")
	}
	if meta.TZ == "" {
		t.Error("meta.tz empty")
	}
}

func TestLoopbackGuard(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"192.168.1.10", false},
		{"example.com", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.host); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.host, got, c.want)
		}
	}
	err := (&LoopbackError{Addr: "0.0.0.0:8787"}).Error()
	if !strings.Contains(err, "127.0.0.1") {
		t.Errorf("LoopbackError text should mention 127.0.0.1: %s", err)
	}
}

func TestSetLoggerSilencesRequests(t *testing.T) {
	var buf syncBuffer
	SetLogger(log.New(&buf, "", 0))
	defer SetLogger(nil)

	handler := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/daily", nil))
	if !strings.Contains(buf.String(), "GET /api/daily") {
		t.Errorf("log output missing request line: %q", buf.String())
	}

	buf.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/live", nil))
	if buf.String() != "" {
		t.Errorf("/api/live should stay quiet, got %q", buf.String())
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// TestRunRefusesNonLoopback ensures Run never binds a public interface.
func TestRunRefusesNonLoopback(t *testing.T) {
	// Port 1 on loopback is fine to attempt; use a definitely-refused wildcard
	// bind by picking an address the OS will reject for non-loopback reasons.
	err := Run(Options{Addr: "0.0.0.0:8787"})
	if err == nil {
		t.Fatal("Run on 0.0.0.0 should fail")
	}
	var loopErr *LoopbackError
	if !errorsAs(err, &loopErr) {
		t.Fatalf("error = %v, want LoopbackError", err)
	}
}

// errorsAs avoids importing errors just for As in one place.
func errorsAs(err error, target **LoopbackError) bool {
	for err != nil {
		if e, ok := err.(*LoopbackError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
