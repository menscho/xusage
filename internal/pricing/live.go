// Live pricing: fetch the LiteLLM pricing table over the network, cache it to
// disk, apply user overrides, and fall back to the embedded snapshot.
//
// The embedded snapshot (see snapshot/litellm_pricing.json) is the offline
// floor — it is always loaded first and restored whenever the live path cannot
// produce a usable table. Refresh() and StartAutoRefresh() are the entry points
// the CLI wires up; init() deliberately does NOT touch the network so that tests
// and offline startup stay fast and deterministic (see AGENTS.md).
package pricing

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	litellmURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

	// DefaultCacheTTL is how long a cached live snapshot is considered fresh.
	DefaultCacheTTL = time.Hour

	// fetchTimeout bounds a single live fetch. The LiteLLM JSON is large, but
	// the dashboard should not hang on a slow link — a short timeout fails fast
	// and falls back to the embedded snapshot.
	fetchTimeout = 10 * time.Second
)

// logf is the package logger. It defaults to the stdlib logger (matching the
// "[xusage]" prefix used across the codebase) but is a variable so tests can
// silence or capture it.
var logf = func(format string, args ...any) {
	log.Printf("[xusage] "+format, args...)
}

// cacheDirOverride and overrideDirOverride let tests redirect the cache and
// override files into t.TempDir() instead of the user's real config/cache dirs.
// Empty means "use the OS default".
var (
	cacheDirOverride    string
	overrideDirOverride string
)

// cachePath returns the OS cache file for the live pricing snapshot
// (~/.cache/xusage/pricing.json, or the platform equivalent).
func cachePath() string {
	if cacheDirOverride != "" {
		return filepath.Join(cacheDirOverride, "pricing.json")
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "xusage", "pricing.json")
}

// overridePath returns the user's custom pricing override file
// (~/.config/xusage/pricing.json, or the platform equivalent).
func overridePath() string {
	if overrideDirOverride != "" {
		return filepath.Join(overrideDirOverride, "pricing.json")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "xusage", "pricing.json")
}

// readCache returns the cached live snapshot when it exists and is within ttl.
// A malformed cache is reported as a miss so the caller can refetch.
func readCache(ttl time.Duration) ([]byte, bool) {
	p := cachePath()
	info, err := os.Stat(p)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > ttl {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return data, true
}

// writeCache atomically best-efforts the live snapshot to disk. A write failure
// is non-fatal — the in-memory table is still updated, we just won't have a
// cache for next startup.
func writeCache(data []byte) error {
	p := cachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// readOverrides returns the user's custom pricing override JSON when the file
// exists. It is not validated here — applyAll surfaces a parse error.
func readOverrides() ([]byte, bool) {
	data, err := os.ReadFile(overridePath())
	if err != nil {
		return nil, false
	}
	return data, true
}

// fetchLive GETs the LiteLLM pricing JSON with a bounded timeout.
func fetchLive(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// refreshMu serializes Refresh() so concurrent callers (the CLI trigger plus the
// auto-refresh goroutine) do not issue overlapping fetches.
var refreshMu sync.Mutex

// Refresh rebuilds the active pricing table from the best available source and
// stores it:
//
//  1. A fresh on-disk cache (within DefaultCacheTTL) if present and parseable.
//  2. Otherwise a live fetch from LiteLLM, cached to disk on success.
//  3. On any network or parse failure, the embedded snapshot.
//
// Custom overrides from overridePath() are applied on top in every case. It is
// safe for concurrent use. A total failure (only possible if the embedded
// snapshot itself is unusable) leaves the previous table intact and returns the
// error.
func Refresh() error {
	return refreshWith(DefaultCacheTTL, litellmURL)
}

// refreshWith is Refresh parameterized for tests (custom ttl + url).
func refreshWith(ttl time.Duration, url string) error {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	// 1. Fresh cache — no network. A malformed cache is treated as a miss so we
	// refetch rather than shipping bad data.
	if data, ok := readCache(ttl); ok {
		if err := applyAll(data); err == nil {
			return nil
		}
		logf("pricing: cached snapshot malformed; attempting live fetch")
	}

	// 2. Live fetch.
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	data, err := fetchLive(ctx, url)
	if err != nil {
		logf("pricing: live fetch failed (%v); using embedded snapshot", err)
		return applyAll(nil)
	}
	_ = writeCache(data) // best-effort; ignore write errors
	if err := applyAll(data); err != nil {
		logf("pricing: live snapshot malformed (%v); using embedded snapshot", err)
		return applyAll(nil)
	}
	return nil
}

// applyAll builds the active table: embedded snapshot (always the base), then a
// live overlay (when non-nil), then custom overrides, and stores the result.
// When live is nil this restores the embedded snapshot with overrides applied.
func applyAll(live []byte) error {
	t := &Table{rates: map[string]Rates{}}
	if err := loadInto(t, snapshotJSON); err != nil {
		return err
	}
	if len(live) > 0 {
		if err := loadInto(t, live); err != nil {
			return err
		}
	}
	if ov, ok := readOverrides(); ok {
		// A malformed override file is a user error, not a fatal one: log and
		// skip it rather than discarding the whole (embedded + live) table.
		if err := loadInto(t, ov); err != nil {
			logf("pricing: override file unparseable (%v); ignoring", err)
		}
	}
	current.Store(t)
	return nil
}

// Auto-refresh background goroutine.

var (
	autoRefreshMu   sync.Mutex
	autoRefreshStop chan struct{}
)

// StartAutoRefresh launches a background goroutine that calls Refresh() every
// interval (defaulting to DefaultCacheTTL when <= 0). It is idempotent: a
// second call is a no-op until StopAutoRefresh is called. The goroutine exits
// when StopAutoRefresh is called or the process exits.
func StartAutoRefresh(interval time.Duration) {
	if interval <= 0 {
		interval = DefaultCacheTTL
	}
	autoRefreshMu.Lock()
	defer autoRefreshMu.Unlock()
	if autoRefreshStop != nil {
		return // already running
	}
	stop := make(chan struct{})
	autoRefreshStop = stop
	go func() {
		// Refresh once up front so the live/override table is populated before
		// the first tick elapses, then on every interval thereafter.
		_ = Refresh()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = Refresh()
			case <-stop:
				return
			}
		}
	}()
}

// StopAutoRefresh halts the background goroutine started by StartAutoRefresh.
// It is safe to call when no refresh is running.
func StopAutoRefresh() {
	autoRefreshMu.Lock()
	defer autoRefreshMu.Unlock()
	if autoRefreshStop != nil {
		close(autoRefreshStop)
		autoRefreshStop = nil
	}
}
