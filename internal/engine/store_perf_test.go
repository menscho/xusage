// Package engine — performance conformance tests for SPEC §10.
//
// Gated behind XUSAGE_PERF=1 so the normal gate stays fast and hermetic:
//
//	XUSAGE_PERF=1 go test -run TestPerfColdLoad50MB -count=1 ./internal/engine
//	XUSAGE_PERF=1 go test -run TestPerfWarmAggregationP99 -count=1 ./internal/engine
//
// The corpus (~50MB, ~1500 files, ~100k entries) is generated once by
// internal/fixtures into .tmp/perf-data (gitignored scratch) and reused across
// runs; the engine is driven through its real discovery→parse→price→dedup
// pipeline with every adapter home pinned to an empty directory except claude's.
package engine

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"xusage/internal/fixtures"

	// Direct claude registration: the perf corpus is claude-shaped, and
	// importing the adapter directly (instead of the internal/sources/all hub)
	// keeps this test independent of other agents' mid-flight adapter work.
	_ "xusage/internal/sources/claude"
)

// perfOverrideEnvs lists every adapter override cleared while the perf corpus
// is loaded, so no real user data can leak into the measurement (same list as
// internal/sources/all/discover_hermetic_test.go).
var perfOverrideEnvs = []string{
	"PI_AGENT_DIR", "GOOSE_PATH_ROOT", "OPENCLAW_DIR", "KILO_DATA_DIR",
	"KIMI_DATA_DIR", "QWEN_DATA_DIR", "GEMINI_DATA_DIR", "GROK_HOME",
	"COPILOT_OTEL_FILE_EXPORTER_PATH", "CODEX_HOME",
	"OPENCODE_DATA_DIR", "AMP_DIR", "CODEBUFF_DB_PATH", "DROID_SESSIONS_DIR",
	"HERMES_HOME",
}

// skipUnlessPerf skips the test unless XUSAGE_PERF=1.
func skipUnlessPerf(t *testing.T) {
	t.Helper()
	if os.Getenv("XUSAGE_PERF") != "1" {
		t.Skip("XUSAGE_PERF=1 not set; skipping SPEC §10 perf conformance")
	}
}

// isolateHomes pins HOME/USERPROFILE at an empty temp dir and clears every
// adapter override. Callers then point CLAUDE_CONFIG_DIR at fixture data.
func isolateHomes(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, env := range perfOverrideEnvs {
		t.Setenv(env, "")
	}
}

// perfCorpusDir returns the throwaway corpus root used by both perf tests
// within one process: generated once, reused across tests, removed at exit.
func perfCorpusDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), ".tmp", "perf-data")
	if info, err := os.Stat(filepath.Join(dir, "claude", "projects")); err != nil || !info.IsDir() {
		start := time.Now()
		info2, err := fixtures.WriteCorpus(dir, fixtures.CorpusOpts{
			Days:        60,
			Projects:    4,
			Sessions:    3,
			FilesPerDay: 2,
			MessagesMin: 45,
			MessagesMax: 95,
			WithErrors:  true,
		})
		if err != nil {
			t.Fatalf("generating perf corpus: %v", err)
		}
		var bytes int64
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if fi, e := d.Info(); e == nil {
					bytes += fi.Size()
				}
			}
			return nil
		})
		t.Logf("perf corpus: %d files, %d entries, %.1f MB in %v (%s)",
			info2.Files, info2.Entries, float64(bytes)/1e6, time.Since(start), dir)
	}
	return dir
}

// repoRoot walks up from the package dir to the xusage module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for !strings.HasSuffix(filepath.ToSlash(dir), "/internal/engine") {
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root with internal/engine not found")
		}
		dir = parent
	}
	return strings.TrimSuffix(dir, string(filepath.Separator)+"internal"+string(filepath.Separator)+"engine")
}

// loadPerfSnapshot points CLAUDE_CONFIG_DIR at the corpus and performs one
// full cold Load(). The returned cleanup restores global state.
func loadPerfSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	isolateHomes(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(perfCorpusDir(t), "claude"))
	resetGlobalSnapshot(t)
	snap, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return snap
}

// resetGlobalSnapshot restores current/parseCalls after the test finishes.
func resetGlobalSnapshot(t *testing.T) {
	t.Helper()
	prev := Get()
	t.Cleanup(func() {
		current.Store(prev)
		parseCalls.Store(0)
	})
}

// TestPerfColdLoad50MB pins SPEC §10's cold-load target: a full scan of a
// ~50MB JSONL corpus must finish in ≤ 2.5s including parse, pricing, dedup,
// sort, and snapshot assembly.
func TestPerfColdLoad50MB(t *testing.T) {
	skipUnlessPerf(t)
	loadPerfSnapshot(t)

	parseCalls.Store(0)
	start := time.Now()
	snap := loadAll(nil)
	cold := time.Since(start)

	parsed := parseCalls.Load()
	if parsed == 0 {
		t.Fatal("cold load reparsed zero files — corpus was not discovered")
	}
	t.Logf("cold load: %v for %d files (%d entries) [target ≤ 2.5s]",
		cold, parsed, len(snap.Entries))
	if cold > 2500*time.Millisecond {
		t.Errorf("cold load of %d files took %v, target is ≤ 2.5s", parsed, cold)
	}
}

// percentile returns the nearest-rank p-th sample of a sorted slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p / 100 * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// TestPerfWarmAggregationP99 pins SPEC §10's warm-API target: repeated
// aggregation calls over an already-loaded snapshot must answer with a p99
// of ≤ 25ms. Daily/Summary/Blocks/ByModel are timed separately so a slow
// endpoint cannot hide behind fast ones.
func TestPerfWarmAggregationP99(t *testing.T) {
	skipUnlessPerf(t)
	snap := loadPerfSnapshot(t)

	const runs = 200
	now := time.Now()
	type endpoint struct {
		name    string
		measure func(i int) time.Duration
	}
	endpoints := []endpoint{
		{"Daily", func(int) time.Duration {
			s := time.Now()
			rows := snap.Daily(&Opts{})
			_ = rows
			return time.Since(s)
		}},
		{"Summary", func(int) time.Duration {
			s := time.Now()
			sum := snap.Summary(&Opts{})
			_ = sum
			return time.Since(s)
		}},
		{"Blocks", func(i int) time.Duration {
			s := time.Now()
			blocks := snap.Blocks(&Opts{}, DefaultBlockHours, now.Add(time.Duration(i)*time.Second))
			_ = blocks
			return time.Since(s)
		}},
		{"ByModel", func(int) time.Duration {
			s := time.Now()
			rows := snap.ByModel(&Opts{})
			_ = rows
			return time.Since(s)
		}},
	}

	fails := 0
	for _, ep := range endpoints {
		durs := make([]time.Duration, 0, runs)
		for i := range runs {
			durs = append(durs, ep.measure(i))
		}
		sort.Slice(durs, func(a, b int) bool { return durs[a] < durs[b] })
		p50 := percentile(durs, 50)
		p99 := percentile(durs, 99)
		maxD := durs[len(durs)-1]
		t.Logf("warm %-8s p50=%-12v p99=%-12v max=%v over %d runs [p99 target ≤ 25ms] (%d entries)",
			ep.name, p50, p99, maxD, runs, len(snap.Entries))
		if p99 > 25*time.Millisecond {
			fails++
		}
	}
	if fails > 0 {
		t.Errorf("%d endpoint(s) exceeded the 25ms warm p99 target", fails)
	}
}

// TestPerfIncrementalRefreshReusesFiles verifies the third §10 clause:
// a steady-state refresh reuses every unchanged file (zero reparses).
func TestPerfIncrementalRefreshReusesFiles(t *testing.T) {
	skipUnlessPerf(t)
	loadPerfSnapshot(t)

	before := ParseCalls()
	Refresh()
	reparsed := ParseCalls() - before
	if reparsed != 0 {
		t.Errorf("steady-state Refresh reparsed %d files, want 0", reparsed)
	}
}
