package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// stubState is the shared backing store of the stub source registered below.
// Tests point it at a directory and mutate files between refresh cycles.
var stubState struct {
	mu       sync.Mutex
	dir      string
	failPath string // parser returns an error for exactly this file
}

// registerStubSource installs a deterministic test source. Re-registering the
// same Name replaces any earlier registration (see sources.Register).
func registerStubSource(t *testing.T) {
	t.Helper()
	stubState.mu.Lock()
	stubState.dir = t.TempDir()
	stubState.failPath = ""
	stubState.mu.Unlock()
	sources.Register(sources.Source{
		Name: "stub",
		Discover: func() ([]string, error) {
			stubState.mu.Lock()
			dir := stubState.dir
			stubState.mu.Unlock()
			if _, err := os.Stat(dir); err != nil {
				return nil, nil // missing data dir is normal, not an error
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, nil
			}
			var paths []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
					paths = append(paths, filepath.Join(dir, e.Name()))
				}
			}
			return paths, nil
		},
		Parse: func(path string) ([]model.Entry, error) {
			stubState.mu.Lock()
			fail := stubState.failPath == path
			stubState.mu.Unlock()
			if fail {
				return nil, errors.New("boom")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var out []model.Entry
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue // malformed lines are skipped, never fatal
				}
				parts := strings.Fields(line)
				if len(parts) != 4 {
					continue
				}
				sec, err1 := parseUnix(parts[0])
				in, err2 := parseCount(parts[2])
				outTok, err3 := parseCount(parts[3])
				if err1 != nil || err2 != nil || err3 != nil {
					continue
				}
				out = append(out, model.Entry{
					ID:           parts[1],
					Timestamp:    time.Unix(sec, 0).UTC(),
					Model:        "stub-model",
					InputTokens:  in,
					OutputTokens: outTok,
					Messages:     1,
				})
			}
			return out, nil
		},
	})
}

func parseUnix(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("bad unix ts")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

func parseCount(s string) (uint64, error) {
	n, err := parseUnix(s)
	return uint64(n), err
}

// writeStubFile creates or replaces one JSONL fixture with the given entries,
// then pins its mtime so incremental-refresh tests are deterministic even on
// coarse-granularity Windows filesystems.
func writeStubFile(t *testing.T, name string, modTime time.Time, lines ...string) string {
	t.Helper()
	stubState.mu.Lock()
	dir := stubState.dir
	stubState.mu.Unlock()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return p
}

// stubLine renders one fixture line: "<unixSec> <id> <in> <out>".
func stubLine(id string, at time.Time, in, out uint64) string {
	return fmt.Sprintf("%d %s %d %d", at.Unix(), id, in, out)
}

var _ = model.Entry{} // keep model import anchored if fixtures change

func TestLoadParsesAllFilesAndSetsProvider(t *testing.T) {
	registerStubSource(t)
	base := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	writeStubFile(t, "a.jsonl", base,
		stubLine("m1", base, 100, 10),
		stubLine("m2", base.Add(time.Minute), 200, 20),
		"garbage line that is skipped",
	)
	writeStubFile(t, "b.jsonl", base, stubLine("m3", base.Add(2*time.Minute), 300, 30))

	snap, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(snap.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (malformed line skipped)", len(snap.Entries))
	}
	for _, e := range snap.Entries {
		if e.Provider != "stub" {
			t.Fatalf("provider = %q, want %q (set by loader)", e.Provider, "stub")
		}
	}
	// Timestamp-sorted ascending after load.
	for i := 1; i < len(snap.Entries); i++ {
		if snap.Entries[i].Timestamp.Before(snap.Entries[i-1].Timestamp) {
			t.Fatal("entries not sorted by timestamp")
		}
	}

	var st *ProviderStatus
	for i := range snap.Providers {
		if snap.Providers[i].Name == "stub" {
			st = &snap.Providers[i]
		}
	}
	if st == nil || !st.Detected || st.FileCount != 2 {
		t.Fatalf("provider status = %+v, want detected with 2 files", st)
	}
}

func TestLoadMissingDirIsEmptyNotError(t *testing.T) {
	registerStubSource(t)
	stubState.mu.Lock()
	os.RemoveAll(stubState.dir) // data dir absent entirely
	stubState.mu.Unlock()

	snap, err := Load()
	if err != nil {
		t.Fatalf("Load() with missing dir returned error %v, want nil", err)
	}
	if len(snap.Entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(snap.Entries))
	}
	for _, p := range snap.Providers {
		if p.Name == "stub" && p.Detected {
			t.Fatal("missing dir must not be reported as detected")
		}
	}
}

func TestRefreshReparsesOnlyChangedFiles(t *testing.T) {
	registerStubSource(t)
	base := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	later := base.Add(3 * time.Hour)
	writeStubFile(t, "one.jsonl", base, stubLine("x1", base, 10, 1))
	writeStubFile(t, "two.jsonl", base, stubLine("y1", base, 20, 2))

	snap, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entries) != 2 {
		t.Fatalf("initial entries = %d, want 2", len(snap.Entries))
	}

	before := ParseCalls()

	// Touch neither file: a refresh should reparse nothing.
	after := Refresh()
	if got := ParseCalls(); got != before {
		t.Fatalf("unchanged poll reparsed %d files, want 0", got-before)
	}
	if len(after.Entries) != 2 {
		t.Fatalf("entries after no-op refresh = %d, want 2", len(after.Entries))
	}

	// Modify only one file (new content AND new mtime): exactly one reparse.
	before = ParseCalls()
	writeStubFile(t, "one.jsonl", later, stubLine("x1", base, 10, 1), stubLine("x2", later, 40, 4))
	after = Refresh()
	if got := ParseCalls(); got-before != 1 {
		t.Fatalf("changed poll reparsed %d files, want 1", got-before)
	}
	if len(after.Entries) != 3 {
		t.Fatalf("entries after change = %d, want 3", len(after.Entries))
	}

	// A file deleted from disk drops out of the snapshot.
	before = ParseCalls()
	stubState.mu.Lock()
	os.Remove(filepath.Join(stubState.dir, "two.jsonl"))
	stubState.mu.Unlock()
	after = Refresh()
	if got := ParseCalls(); got != before {
		t.Fatal("removal pass should not reparse surviving files")
	}
	if len(after.Entries) != 2 {
		t.Fatalf("entries after removal = %d, want 2", len(after.Entries))
	}
}

func TestRefreshSameContentNewMtimeReparsesOnce(t *testing.T) {
	registerStubSource(t)
	base := time.Date(2026, 5, 3, 7, 0, 0, 0, time.UTC)
	writeStubFile(t, "same.jsonl", base, stubLine("z1", base, 5, 5))

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	before := ParseCalls()
	later := base.Add(time.Hour)
	writeStubFile(t, "same.jsonl", later, stubLine("z1", base, 5, 5)) // same bytes, new mtime
	Refresh()
	if got := ParseCalls(); got-before != 1 {
		t.Fatalf("mtime bump should force exactly 1 reparse, got %d", got-before)
	}
}

func TestParseErrorIsolatesFile(t *testing.T) {
	registerStubSource(t)
	base := time.Date(2026, 5, 4, 6, 0, 0, 0, time.UTC)
	good := writeStubFile(t, "good.jsonl", base, stubLine("g1", base, 50, 5))
	bad := writeStubFile(t, "bad.jsonl", base, stubLine("b1", base, 60, 6))
	stubState.mu.Lock()
	stubState.failPath = bad
	stubState.mu.Unlock()

	snap, err := Load()
	if err != nil {
		t.Fatalf("per-file parse failure must not fail the load: %v", err)
	}
	if len(snap.Entries) != 1 || snap.Entries[0].ID != "g1" {
		t.Fatalf("expected only good.jsonl's entry, got %+v", snap.Entries)
	}
	_ = good

	// Healing the file makes it come back on the next refresh.
	stubState.mu.Lock()
	stubState.failPath = ""
	stubState.mu.Unlock()
	writeStubFile(t, "bad.jsonl", base.Add(time.Minute), stubLine("b1", base, 60, 6))
	snap = Refresh()
	found := false
	for _, e := range snap.Entries {
		if e.ID == "b1" {
			found = true
		}
	}
	if !found {
		t.Fatal("recovered file did not appear after refresh")
	}
}

func TestPriceResolvesCostVariants(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		reported    *float64
		in          uint64
		wantDisplay float64
		wantAutoGt0 bool
		wantCalcGt0 bool
		wantMissing bool
	}{
		{
			name:        "known priced model computes all variants",
			model:       "claude-sonnet-4-20250514",
			in:          1_000_000,
			wantDisplay: 0, wantAutoGt0: true, wantCalcGt0: true,
		},
		{
			name:        "unknown model is zero cost and flagged missing pricing",
			model:       "totally-unknown-model",
			in:          1_000_000,
			wantDisplay: 0, wantMissing: true,
		},
		{
			name:        "reported cost drives display and auto",
			model:       "claude-sonnet-4-20250514",
			reported:    ptr(0.42),
			in:          1_000,
			wantDisplay: 0.42, wantAutoGt0: true, wantCalcGt0: true,
		},
		{
			name:        "zero tokens stay zero cost",
			model:       "claude-sonnet-4-20250514",
			wantDisplay: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ents := []model.Entry{{
				ID:           "p1",
				Timestamp:    time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
				Model:        tt.model,
				InputTokens:  tt.in,
				ReportedCost: tt.reported,
				Messages:     1,
			}}
			price(ents)
			e := ents[0]
			if e.CostDisplay != tt.wantDisplay {
				t.Fatalf("CostDisplay = %v, want %v", e.CostDisplay, tt.wantDisplay)
			}
			if tt.wantAutoGt0 && e.CostAuto <= 0 {
				t.Fatalf("CostAuto = %v, want > 0", e.CostAuto)
			}
			if !tt.wantAutoGt0 && tt.reported == nil && e.CostAuto != 0 {
				t.Fatalf("CostAuto = %v, want computed-or-zero", e.CostAuto)
			}
			if tt.wantCalcGt0 && e.CostCalc <= 0 {
				t.Fatalf("CostCalc = %v, want > 0", e.CostCalc)
			}
			if e.MissingPricing != tt.wantMissing {
				t.Fatalf("MissingPricing = %v, want %v", e.MissingPricing, tt.wantMissing)
			}
		})
	}
}

func ptr(f float64) *float64 { return &f }

func TestConcurrentGetSafety(t *testing.T) {
	registerStubSource(t)
	base := time.Date(2026, 5, 6, 5, 0, 0, 0, time.UTC)
	writeStubFile(t, "race.jsonl", base, stubLine("r1", base, 70, 7))
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // writer: keeps swapping snapshots
		defer wg.Done()
		n := base
		for {
			select {
			case <-stop:
				return
			default:
			}
			n = n.Add(time.Second)
			writeStubFile(t, "race.jsonl", n, stubLine(fmt.Sprintf("r%d", n.Unix()), n, 80, 8))
			Refresh()
		}
	}()
	for i := 0; i < 4; i++ { // readers: hammer Get + aggregate concurrently
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := Get()
				sum := snap.Summary(&Opts{})
				if sum.Messages < 0 {
					t.Error("negative messages")
				}
			}
		}()
	}
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestSnapshotDoesNotAliasPreviousEntries(t *testing.T) {
	registerStubSource(t)
	base := time.Date(2026, 5, 7, 4, 0, 0, 0, time.UTC)
	writeStubFile(t, "alias.jsonl", base, stubLine("a1", base, 90, 9))
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	writeStubFile(t, "alias.jsonl", base.Add(time.Hour), stubLine("a1", base, 90, 9), stubLine("a2", base.Add(time.Minute), 91, 9))
	second := Refresh()

	// The old snapshot must be untouched by the new load.
	if len(first.Entries) != 1 {
		t.Fatalf("old snapshot mutated: %d entries, want 1", len(first.Entries))
	}
	if len(second.Entries) != 2 {
		t.Fatalf("new snapshot entries = %d, want 2", len(second.Entries))
	}
}
