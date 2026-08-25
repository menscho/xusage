package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"xusage/internal/pricing"
	"xusage/internal/server"
)

// adaptiveBackstop is handed to the server's built-in fixed-interval ticker.
// The server cannot disable its own ticker from here (engine.StartPolling always
// spawns one and exposes no off switch), so cmd's adaptive loop below is the
// primary refresh driver and the server only ticks as a slow safety net: if the
// adaptive goroutine ever stalled, data stays no more than this stale. It is
// chosen well above the loosest adaptive tier so it never tightens relative to
// backoff.
const adaptiveBackstop = 5 * time.Minute

// version is overridden at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// flags holds the parsed command line.
type flags struct {
	addr    string
	poll    time.Duration
	open    bool
	show    bool
	procs   int  // GOMAXPROCS; 0 = auto (a sensible default)
	refresh bool // refresh pricing now and exit (no server)
}

func parseFlags() (*flags, error) {
	f := &flags{}
	flag.StringVar(&f.addr, "addr", "127.0.0.1:8787", "HTTP bind address (loopback only)")
	flag.DurationVar(&f.poll, "poll", 2*time.Second, "background data refresh interval (adaptive floor)")
	flag.BoolVar(&f.open, "open", false, "open the dashboard in the default browser")
	flag.BoolVar(&f.show, "version", false, "print version and exit")
	flag.IntVar(&f.procs, "procs", 0, "GOMAXPROCS; 0 = auto (min(NumCPU, 8)); an explicit value always wins")
	flag.BoolVar(&f.refresh, "refresh-pricing", false, "refresh pricing data now and exit (no server)")
	flag.Parse()
	return f, nil
}

// applyRuntimeTuning sets GOMAXPROCS and GOGC to sensible defaults when the
// user hasn't overridden them.
//
// GOMAXPROCS: the engine's parser already caps its worker pool at
// min(GOMAXPROCS, 8), so it never needs more than 8 OS threads; extra cores
// would only add scheduler and GC threads (and their stacks) for no benefit on
// a single-tenant dashboard. Capping at min(NumCPU, 8) trims that steady-state
// thread count — and per-thread stack memory — with zero parsing downside.
//
// GOGC: the dashboard holds a stable long-lived heap (the loaded snapshot) and
// idles between polls with almost no allocation. The default GOGC of 100 tells
// the GC to trigger at 2x live heap — so the transient double-heap spike that
// occurs while a refresh builds the new snapshot alongside the old one trips a
// collection that scans the (nearly garbage-free) snapshot for little gain.
// Bumping GOGC to 200 raises that threshold to ~3x live, letting the spike pass
// without a GC; collection then runs only when real garbage has accumulated.
// This trades a bounded, transient rise in peak RSS during a refresh for fewer
// GC cycles scanning a stable heap — a net CPU win for an idle-mostly daemon.
func applyRuntimeTuning(procs int) {
	if procs <= 0 {
		procs = runtime.NumCPU()
		if procs > 8 {
			procs = 8
		}
	}
	runtime.GOMAXPROCS(procs)
	debug.SetGCPercent(200)
}

// run parses flags, starts the server, and returns the process exit error.
func run() error {
	f, err := parseFlags()
	if err != nil {
		return err
	}
	if f.show {
		fmt.Println(fullVersion())
		return nil
	}
	if f.refresh {
		if err := pricing.Refresh(); err != nil {
			return fmt.Errorf("pricing refresh failed: %w (using embedded snapshot)", err)
		}
		fmt.Println("pricing refreshed")
		return nil
	}

	applyRuntimeTuning(f.procs) // trim scheduler/GC threads before the engine starts

	// Refresh pricing from LiteLLM (falls back to the embedded snapshot when the
	// network is unreachable) and keep it fresh in the background. A warning is
	// logged on failure; the dashboard always has a usable table.
	go func() {
		if err := pricing.Refresh(); err != nil {
			// best-effort; embedded snapshot remains the fallback
		}
		pricing.StartAutoRefresh(pricing.DefaultCacheTTL)
	}()

	server.Version = version // surfaced by /api/meta and the dashboard footer

	// Own the refresh cadence with an adaptive poller and hand the server only a
	// slow backstop ticker (see adaptiveBackstop). engine.StartPolling cannot be
	// suppressed from here, so the server's fixed tick must never be the fast
	// path: cadence lives in the adaptive loop, the server is just a safety net.
	stopPoll := make(chan struct{})
	defer close(stopPoll)
	go newAdaptivePoller(f.poll).run(stopPoll)

	return server.Run(server.Options{
		Addr:           f.addr,
		PollInterval:   adaptiveBackstop,
		RefreshOnStart: true,
		OnReady: func(url string) {
			if f.open {
				openBrowser(url)
			}
			printStartup(url)
		},
	})
}

func osExit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "xusage:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
