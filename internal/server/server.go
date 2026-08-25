// Package server exposes the local HTTP API and the embedded dashboard.
//
// Handlers read the engine's atomic snapshot only — no locking, no per-request
// parsing — so responses stay instant regardless of data size. The server binds
// the loopback interface only; it is a local development tool, not a service.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // embed IANA zones: tz= query param must work on Windows

	"xusage/internal/engine"
	"xusage/internal/model"
)

// Options configures the server.
type Options struct {
	Addr           string        // default 127.0.0.1:8787
	PollInterval   time.Duration // background refresh cadence; 0 = 2s
	RefreshOnStart bool          // perform an initial engine.Load before serving
	// OnReady runs once the listener is accepting connections, with the
	// serving URL (e.g. "http://127.0.0.1:8787"). May be nil.
	OnReady func(url string)
}

// Run starts the poller and serves HTTP until the process receives SIGINT or
// SIGTERM, then shuts down gracefully with a bounded drain window.
func Run(opts Options) error {
	return runWithOptions(opts, nil)
}

// runWithOptions is Run with an injectable interrupt source so tests can
// exercise graceful shutdown deterministically (real signals are unreliable
// across processes on Windows). A nil interrupt uses SIGINT/SIGTERM.
func runWithOptions(opts Options, interrupt <-chan struct{}) error {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8787"
	}
	interval := opts.PollInterval
	if interval == 0 {
		interval = 2 * time.Second
	}

	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if host, _, err := net.SplitHostPort(listener.Addr().String()); err == nil && !isLoopback(host) {
		return &LoopbackError{Addr: opts.Addr} // refuse before touching any data
	}

	// Serve before the first scan: handlers read the atomic snapshot only,
	// so requests see an empty (then progressively fresher) dashboard instead
	// of connecting to a socket nobody drains while big data loads.
	srv := &http.Server{
		Handler:           logRequests(NewHandler()),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[xusage] http server: %v", err)
		}
	}()

	if opts.OnReady != nil {
		url := "http://" + listener.Addr().String()
		if host, port, err := net.SplitHostPort(listener.Addr().String()); err == nil &&
			(host == "0.0.0.0" || host == "[::]") {
			url = "http://127.0.0.1:" + port
		}
		opts.OnReady(url)
	}

	if opts.RefreshOnStart {
		_, _ = engine.Load()
	} else {
		engine.Refresh()
	}
	engine.StartPolling(interval)
	defer engine.StopPolling()

	ctx := interruptContext(interrupt)
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// interruptContext yields the context Run waits on: fired by the injected
// interrupt channel (tests) or by SIGINT/SIGTERM (production).
func interruptContext(interrupt <-chan struct{}) context.Context {
	if interrupt != nil {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			defer cancel()
			select {
			case <-interrupt:
			case <-ctx.Done():
			}
		}()
		return ctx
	}
	ctx, stop := signal.NotifyContext(context.Background(), osInterrupt...)
	go func() { // release the signal handler once shutdown fired or a signal came
		<-ctx.Done()
		stop()
	}()
	return ctx
}

// osSignals lists the interrupt signals Run shuts down on. On Windows only
// os.Interrupt is delivered reliably; SIGTERM is included where supported.
func osSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// LoopbackError reports an attempt to bind a non-loopback address.
type LoopbackError struct{ Addr string }

func (e *LoopbackError) Error() string {
	return "xusage binds 127.0.0.1 only (local dashboard), refusing " + e.Addr
}

// registerRoutes wires every API endpoint plus the embedded dashboard.
// Each route also registers a method-less fallback so unsupported methods get
// a JSON 405 instead of the stdlib mux's plain-text response, and the /api/
// subtree gets a JSON 404 for unknown endpoints.
func registerRoutes(mux *http.ServeMux) {
	routes := []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/meta", handleMeta},
		{"GET /api/summary", handleSummary},
		{"GET /api/daily", rowHandler(func(s *engine.Snapshot, o *engine.Opts) any { return s.Daily(o) })},
		{"GET /api/weekly", rowHandler(func(s *engine.Snapshot, o *engine.Opts) any { return s.Weekly(o) })},
		{"GET /api/monthly", rowHandler(func(s *engine.Snapshot, o *engine.Opts) any { return s.Monthly(o) })},
		{"GET /api/hourly", rowHandler(func(s *engine.Snapshot, o *engine.Opts) any { return s.Hourly(o) })},
		{"GET /api/models", handleModels},
		{"GET /api/providers", handleProviders},
		{"GET /api/pulse", handlePulse},
		{"GET /api/sessions", handleSessions},
		{"GET /api/projects", rowHandler(func(s *engine.Snapshot, o *engine.Opts) any { return s.ByProject(o) })},
		{"GET /api/blocks", handleBlocks},
		{"GET /api/live", handleLive},
		{"POST /api/refresh", handleRefresh},
	}
	for _, rt := range routes {
		mux.HandleFunc(rt.pattern, rt.handler)
		path := strings.TrimPrefix(rt.pattern, "GET ")
		path = strings.TrimPrefix(path, "POST ")
		mux.HandleFunc(path, methodNotAllowed)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "unknown endpoint")
	})
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	fail(w, http.StatusMethodNotAllowed, "method not allowed")
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false // hostnames: only literal loopback names are accepted
	}
	return ip.IsLoopback()
}

var osInterrupt = osSignals()

// logRequests emits one line per request except the live poll, which fires
// every few seconds and would drown everything else out. When no logger is
// installed (the production default), the wrapper is a zero-cost passthrough:
// it skips the timestamp capture and the fmt args slice entirely, so
// steady-state polls allocate nothing for logging.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logger == nil { // common case: no logger installed, log nothing
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/api/live" { // keep the poll quiet
			infof("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// infof logs through the stdlib logger; swap via SetLogger in tests or embeds.
// logRequests only reaches here once logger is known non-nil, so no inner guard.
func infof(format string, args ...any) {
	logger.Printf(format, args...)
}

// logger holds the package logger; nil disables request logging entirely.
var logger *log.Logger

// SetLogger installs the request logger. Passing nil silences logging.
func SetLogger(l *log.Logger) { logger = l }

// writeJSON emits v as a camelCase-tagged JSON body.
//
// The json.Encoder is allocated fresh per call. It is a small, fixed-size
// struct and the bound http.ResponseWriter differs per request, so pooling it
// (via Encoder.Reset) would save only a single tiny heap allocation per
// request — a sub-MB gain not worth the pool complexity at this floor. The
// polled vendor/model paths already skip encoding entirely via qCache.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// errorResponse replaces the per-call map[string]string in fail, removing one
// heap allocation from every error path (405/404). JSON shape is identical.
type errorResponse struct {
	Error string `json:"error"`
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// optsPool reuses *engine.Opts across requests. Aggregators only read the
// pointer during the call and never retain it (verified in aggregate.go:
// Daily/Weekly/.../Summary/BySession/ByProject/ByModel/Blocks/ActiveBlock all
// return owned values), so a handler can borrow a pooled Opts, run its
// aggregation, and return it. This removes one heap allocation from every
// row/summary/sessions/blocks/live request — the largest remaining per-request
// alloc on the polled endpoints. sync.Pool may drop entries under GC, which only
// forfeits the reuse benefit, never correctness.
var optsPool = sync.Pool{
	New: func() any { return &engine.Opts{} },
}

// getOpts borrows a cleared Opts from the pool. Every field is reset because
// optsFromQuery only overwrites params present in the query — absent params
// must not leak stale values from a previous borrower.
func getOpts() *engine.Opts {
	o := optsPool.Get().(*engine.Opts)
	o.TZ = nil
	o.Since = ""
	o.Until = ""
	o.Last = 0
	o.Project = ""
	o.Session = ""
	o.Model = ""
	o.Provider = ""
	o.Vendor = ""
	o.Mode = ""
	return o
}

// putOpts returns o to the pool. Callers must not touch o afterwards.
func putOpts(o *engine.Opts) { optsPool.Put(o) }

// optsFromRequest parses the shared filter query parameters.
func optsFromRequest(r *http.Request) *engine.Opts {
	return optsFromQuery(r.URL.Query())
}

// optsFromQuery builds Opts from an already-parsed url.Values. Handlers that
// need the raw query for extra parameters (e.g. /api/blocks ?active/?limit)
// parse once and call this directly, so the query string is not re-parsed.
// The returned Opts is pooled; the caller must invoke putOpts when done.
func optsFromQuery(q url.Values) *engine.Opts {
	o := getOpts()
	o.Since = q.Get("since")
	o.Until = q.Get("until")
	o.Project = q.Get("project")
	o.Session = q.Get("session")
	o.Model = q.Get("model")
	o.Provider = q.Get("provider")
	o.Mode = model.ParseMode(q.Get("mode"))
	o.Vendor = q.Get("vendor")
	if n, err := strconv.Atoi(q.Get("last")); err == nil && n > 0 {
		o.Last = n
	}
	if tz := q.Get("tz"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			o.TZ = loc
		}
	}
	return o
}

// metaResponse is the fixed-shape /api/meta payload. A struct avoids the
// per-request map + bucket allocation that map[string]any would incur.
type metaResponse struct {
	Version     string                  `json:"version"`
	GeneratedAt string                  `json:"generatedAt"`
	TZ          string                  `json:"tz"`
	Providers   []engine.ProviderStatus `json:"providers"`
}

func handleMeta(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	writeJSON(w, http.StatusOK, metaResponse{
		Version:     Version,
		GeneratedAt: snap.BuiltAt.UTC().Format(time.RFC3339Nano),
		TZ:          time.Local.String(),
		Providers:   snap.Providers,
	})
}

func handleSummary(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	o := optsFromRequest(r)
	defer putOpts(o)
	writeJSON(w, http.StatusOK, snap.Summary(o))
}

// rowHandler adapts a snapshot aggregator returning rows.
func rowHandler(fn func(*engine.Snapshot, *engine.Opts) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o := optsFromRequest(r)
		defer putOpts(o)
		writeJSON(w, http.StatusOK, fn(engine.Get(), o))
	}
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o := optsFromQuery(q)
	defer putOpts(o)
	limit := 200
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	rows := engine.Get().BySession(o)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	writeJSON(w, http.StatusOK, rows)
}

func handleBlocks(w http.ResponseWriter, r *http.Request) {
	// Parse the query once: reuse the same url.Values for both Opts and the
	// ?active/?limit flags so the polled /api/blocks path parses the query
	// string exactly once instead of twice.
	q := r.URL.Query()
	o := optsFromQuery(q)
	defer putOpts(o)
	// Blocks() returns a freshly-owned slice per call, so we filter and reverse
	// in place instead of allocating a second slice. This removes one heap
	// allocation from the polled /api/blocks hot path.
	blocks := engine.Get().Blocks(o, engine.DefaultBlockHours, time.Now())
	if q.Get("active") == "true" {
		n := 0
		for _, b := range blocks {
			if b.IsActive {
				blocks[n] = b
				n++
			}
		}
		blocks = blocks[:n]
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && len(blocks) > n {
		blocks = blocks[len(blocks)-n:] // most recent n
	}
	for i, j := 0, len(blocks)-1; i < j; i, j = i+1, j-1 { // latest first
		blocks[i], blocks[j] = blocks[j], blocks[i]
	}
	if blocks == nil { // keep the [] contract for empty snapshots (allocates only then)
		blocks = []model.Block{}
	}
	writeJSON(w, http.StatusOK, blocks)
}

// liveToday is the fixed-shape "today" sub-object of /api/live. A struct avoids
// the per-request nested map allocation.
type liveToday struct {
	Cost     float64 `json:"cost"`
	Tokens   uint64  `json:"tokens"`
	Messages int64   `json:"messages"`
}

// liveResponse is the fixed-shape /api/live payload. Pointer fields serialize
// as null when nil, matching the previous map-based behavior exactly.
type liveResponse struct {
	UpdatedAt   string       `json:"updatedAt"`
	Today       liveToday    `json:"today"`
	ActiveBlock *model.Block `json:"activeBlock"`
	Latest      *latestEntry `json:"latest"`
}

func handleLive(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	loc := time.Local
	now := time.Now()
	// Compare civil-date components, not a formatted string: the poll loops
	// over every entry, and Format allocates a string per iteration. Date()
	// returns three ints with no allocation (same idiom as last7Series).
	ty, tm, td := now.In(loc).Date()
	var today liveToday
	for i := range snap.Entries {
		e := &snap.Entries[i]
		ey, em, ed := e.Timestamp.In(loc).Date()
		if ey != ty || em != tm || ed != td {
			continue
		}
		today.Cost += e.CostAuto
		today.Tokens += e.TotalTokens()
		today.Messages += int64(max(e.Messages, 1))
	}
	o := getOpts()
	defer putOpts(o)
	writeJSON(w, http.StatusOK, liveResponse{
		UpdatedAt:   now.UTC().Format(time.RFC3339Nano),
		Today:       today,
		ActiveBlock: snap.ActiveBlock(o, now),
		Latest:      latestOf(snap),
	})
}

// refreshResponse is the fixed-shape /api/refresh payload. A struct avoids the
// per-request map + bucket allocation that map[string]any would incur.
type refreshResponse struct {
	GeneratedAt string                  `json:"generatedAt"`
	Entries     int                     `json:"entries"`
	Providers   []engine.ProviderStatus `json:"providers"`
}

func handleRefresh(w http.ResponseWriter, r *http.Request) {
	snap := engine.Refresh()
	writeJSON(w, http.StatusOK, refreshResponse{
		GeneratedAt: snap.BuiltAt.UTC().Format(time.RFC3339Nano),
		Entries:     len(snap.Entries),
		Providers:   snap.Providers,
	})
}

// Version is overridden at build time via -ldflags.
var Version = "dev"
