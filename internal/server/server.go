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
	"os"
	"os/signal"
	"strconv"
	"strings"
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
// every few seconds and would drown everything else out.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/api/live" { // keep the poll quiet
			infof("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// infof logs through the stdlib logger; swap via SetLogger in tests or embeds.
func infof(format string, args ...any) {
	if logger != nil {
		logger.Printf(format, args...)
	}
}

// logger holds the package logger; nil disables request logging entirely.
var logger *log.Logger

// SetLogger installs the request logger. Passing nil silences logging.
func SetLogger(l *log.Logger) { logger = l }

// writeJSON emits v as a camelCase-tagged JSON body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// optsFromRequest parses the shared filter query parameters.
func optsFromRequest(r *http.Request) *engine.Opts {
	q := r.URL.Query()
	o := &engine.Opts{
		Since:    q.Get("since"),
		Until:    q.Get("until"),
		Project:  q.Get("project"),
		Session:  q.Get("session"),
		Model:    q.Get("model"),
		Provider: q.Get("provider"),
		Mode:     model.ParseMode(q.Get("mode")),
	}
	if n, err := strconv.Atoi(q.Get("last")); err == nil && n > 0 {
		o.Last = n
	}
	if tz := q.Get("tz"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			o.TZ = loc
		}
	}
	o.Vendor = q.Get("vendor")
	return o
}

func handleMeta(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     Version,
		"generatedAt": snap.BuiltAt.UTC().Format(time.RFC3339Nano),
		"tz":          time.Local.String(),
		"providers":   snap.Providers,
	})
}

func handleSummary(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	writeJSON(w, http.StatusOK, snap.Summary(optsFromRequest(r)))
}

// rowHandler adapts a snapshot aggregator returning rows.
func rowHandler(fn func(*engine.Snapshot, *engine.Opts) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, fn(engine.Get(), optsFromRequest(r)))
	}
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	o := optsFromRequest(r)
	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	rows := engine.Get().BySession(o)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	writeJSON(w, http.StatusOK, rows)
}

func handleBlocks(w http.ResponseWriter, r *http.Request) {
	o := optsFromRequest(r)
	q := r.URL.Query()
	blocks := engine.Get().Blocks(o, engine.DefaultBlockHours, time.Now())
	out := make([]model.Block, 0, len(blocks))
	for _, b := range blocks {
		if q.Get("active") == "true" && !b.IsActive {
			continue
		}
		out = append(out, b)
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && len(out) > n {
		out = out[len(out)-n:] // most recent n
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // latest first
		out[i], out[j] = out[j], out[i]
	}
	writeJSON(w, http.StatusOK, out)
}

func handleLive(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	loc := time.Local
	now := time.Now()
	todayKey := now.Format("2006-01-02")
	var todayCost float64
	var todayTokens uint64
	var todayMessages int64
	for i := range snap.Entries {
		e := &snap.Entries[i]
		if e.Timestamp.In(loc).Format("2006-01-02") != todayKey {
			continue
		}
		todayCost += e.CostAuto
		todayTokens += e.TotalTokens()
		todayMessages += int64(max(e.Messages, 1))
	}
	payload := map[string]any{
		"updatedAt": now.UTC().Format(time.RFC3339Nano),
		"today": map[string]any{
			"cost":     todayCost,
			"tokens":   todayTokens,
			"messages": todayMessages,
		},
	}
	payload["activeBlock"] = nil // always present; null when no block is live
	if b := snap.ActiveBlock(&engine.Opts{}, now); b != nil {
		payload["activeBlock"] = b
	}
	payload["latest"] = latestOf(snap)
	writeJSON(w, http.StatusOK, payload)
}

func handleRefresh(w http.ResponseWriter, r *http.Request) {
	snap := engine.Refresh()
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": snap.BuiltAt.UTC().Format(time.RFC3339Nano),
		"entries":     len(snap.Entries),
		"providers":   snap.Providers,
	})
}

// Version is overridden at build time via -ldflags.
var Version = "dev"
