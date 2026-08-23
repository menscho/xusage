// Package engine loads usage entries from every registered source into an
// atomically-swapped immutable Snapshot. Readers (HTTP handlers) never lock;
// a background poller reparses only changed files and swaps the snapshot.
//
// Each Snapshot carries a per-file parse cache (path + stat fingerprint +
// priced entries). Refresh reuses a file's cache wholesale when neither its
// mtime nor size changed, so a steady-state poll parses nothing at all.
package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"xusage/internal/model"
	"xusage/internal/pricing"
	"xusage/internal/sources"
)

// ProviderStatus reports whether a source's data directory was found.
type ProviderStatus struct {
	Name      string `json:"name"`
	Detected  bool   `json:"detected"`
	FileCount int    `json:"fileCount"`
}

// fileCache is the parsed state of one data file: the stat fingerprint the
// entries were produced from plus the entries themselves (already priced,
// pre-dedup). Snapshots own their caches; they are never mutated after
// construction.
type fileCache struct {
	path    string
	modTime time.Time
	size    int64
	entries []model.Entry
}

// Snapshot is an immutable view of all loaded, deduplicated, priced entries.
// Snapshots are never mutated after construction; Refresh builds a new one.
type Snapshot struct {
	Entries   []model.Entry
	Providers []ProviderStatus
	BuiltAt   time.Time

	// files backs incremental refresh; never mutated after construction.
	files []fileCache
}

var (
	current       atomic.Pointer[Snapshot]
	emptySnapshot = &Snapshot{BuiltAt: time.Unix(0, 0).UTC()}
)

// parseCalls counts files actually handed to a parser since process start.
// It lets tests assert that an incremental refresh only reparses files whose
// contents changed.
var parseCalls atomic.Int64

// ParseCalls reports how many files have been handed to a parser since
// process start. Test-only observability for incremental refresh.
func ParseCalls() int64 { return parseCalls.Load() }

// Get returns the current snapshot; never nil.
func Get() *Snapshot {
	if s := current.Load(); s != nil {
		return s
	}
	return emptySnapshot
}

// SetForTest installs a snapshot as the current one. Test-only seam so
// handler tests can run against fixture data without touching the filesystem.
func SetForTest(t testingT, s *Snapshot) {
	if t != nil {
		t.Cleanup(func() {
			current.Store(emptySnapshot)
			parseCalls.Store(0)
		})
	}
	current.Store(s)
}

// NewSnapshotForTest builds an immutable snapshot around pre-priced entries.
// Test-only helper for packages that need populated snapshots.
func NewSnapshotForTest(entries []model.Entry) *Snapshot {
	internSessions(entries)
	return &Snapshot{Entries: entries, BuiltAt: time.Now(), files: []fileCache{}}
}

// internSessions assigns every entry a dense 1-based SessionIdx keyed by its
// SessionID, so warm aggregation counts distinct sessions with int-keyed sets
// instead of hashing long ID strings per entry (SPEC §10 budget). Called once
// per snapshot construction; SessionIdx is meaningless on hand-built entries.
func internSessions(entries []model.Entry) {
	idx := make(map[string]int)
	for i := range entries {
		if sid := entries[i].SessionID; sid != "" {
			n := idx[sid]
			if n == 0 {
				n = len(idx) + 1
				idx[sid] = n
			}
			entries[i].SessionIdx = n
		}
	}
}

// testingT is the minimal testing.TB surface SetForTest relies on.
type testingT interface {
	Cleanup(func())
}

// Load performs a full scan of every registered source and stores the result.
func Load() (*Snapshot, error) {
	snap := loadAll(nil)
	current.Store(snap)
	return snap, nil
}

// Refresh rescans sources, reparsing only new or modified files when previous
// state is available, then atomically swaps the snapshot.
func Refresh() *Snapshot {
	snap := loadAll(Get())
	current.Store(snap)
	return snap
}

// pollingStop guards the poller's stop channel so StopPolling can reach the
// goroutine StartPolling spawned; pollingStopped closes the window where a
// tick fires concurrently with shutdown and still triggers one last refresh.
var (
	pollingStop    atomic.Value
	pollingStopped atomic.Bool
)

// StartPolling launches a background goroutine that calls Refresh every
// interval until StopPolling is called or the process exits. A previous,
// not-yet-stopped poller keeps running; callers poll at most once.
func StartPolling(interval time.Duration) {
	ch := make(chan struct{})
	pollingStopped.Store(false)
	pollingStop.Store(ch)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ch:
				return
			case <-ticker.C:
				if pollingStopped.Load() {
					return // stop won the race against this tick
				}
				Refresh()
			}
		}
	}()
}

// StopPolling stops the poller launched by StartPolling, if any. It lets
// server.Run's shutdown path end background rescans instead of leaking them.
// The stopped flag is set before the channel closes, so no surviving tick
// still triggers a refresh.
func StopPolling() {
	pollingStopped.Store(true)
	if ch, ok := pollingStop.Load().(chan struct{}); ok {
		close(ch)
	}
}

// loadJob is one discovered file to reuse or parse during a load pass.
type loadJob struct {
	source  string
	path    string
	modTime time.Time
	size    int64
}

// loadAll discovers files across sources, reuses unchanged per-file caches
// from prev, parses the rest in parallel, dedupes, and assembles the snapshot.
// Costs are resolved per file at parse time, so reused files pay nothing.
func loadAll(prev *Snapshot) *Snapshot {
	var jobs []loadJob
	status := map[string]*ProviderStatus{}

	for _, src := range sources.All() {
		st := &ProviderStatus{Name: src.Name}
		status[src.Name] = st
		paths, err := src.Discover()
		if err != nil {
			continue // discovery failure = not detected
		}
		st.Detected = len(paths) > 0
		st.FileCount = len(paths)
		for _, p := range paths {
			fi, statErr := os.Stat(p)
			if statErr != nil {
				continue // vanished mid-scan; picked up on the next refresh
			}
			jobs = append(jobs, loadJob{
				source:  src.Name,
				path:    p,
				modTime: fi.ModTime(),
				size:    fi.Size(),
			})
		}
	}

	prevByPath := make(map[string]fileCache, len(jobs))
	if prev != nil {
		for _, f := range prev.files {
			prevByPath[f.path] = f
		}
	}

	caches := make([]fileCache, 0, len(jobs))
	var pending []loadJob
	for _, j := range jobs {
		if f, ok := prevByPath[j.path]; ok && f.size == j.size && f.modTime.Equal(j.modTime) {
			caches = append(caches, f)
			continue
		}
		pending = append(pending, j)
	}

	parsePending(pending, &caches)
	sort.Slice(caches, func(i, j int) bool { return caches[i].path < caches[j].path })

	// Flatten into a fresh backing array: snapshots must never share entry
	// storage, or a later sort would reshuffle a snapshot a handler holds.
	total := 0
	for _, c := range caches {
		total += len(c.entries)
	}
	entries := make([]model.Entry, 0, total)
	for _, c := range caches {
		entries = append(entries, c.entries...)
	}
	entries = dedupe(entries)
	sortEntries(entries)
	internSessions(entries)

	providers := make([]ProviderStatus, 0, len(status))
	for _, name := range sources.Names() {
		providers = append(providers, *status[name])
	}
	return &Snapshot{
		Entries:   entries,
		Providers: providers,
		BuiltAt:   time.Now(),
		files:     caches,
	}
}

// parsePending parses jobs with a bounded worker pool, appending one fileCache
// per file to out. Per-file isolation: a bad file yields an empty cache, never
// a failed load.
func parsePending(pending []loadJob, out *[]fileCache) {
	if len(pending) == 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(pending) {
		workers = len(pending)
	}
	if workers > 8 {
		workers = 8
	}

	jobCh := make(chan loadJob)
	cacheCh := make(chan fileCache, len(pending))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				ents := parseFile(j.source, j.path)
				price(ents) // resolve costs once; reused files keep theirs
				cacheCh <- fileCache{
					path:    j.path,
					modTime: j.modTime,
					size:    j.size,
					entries: ents,
				}
			}
		}()
	}
	for _, j := range pending {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	close(cacheCh)
	for c := range cacheCh {
		*out = append(*out, c)
	}
}

// parseFile parses one file with per-file error isolation.
func parseFile(source, path string) []model.Entry {
	parseCalls.Add(1)
	for _, src := range sources.All() {
		if src.Name != source {
			continue
		}
		ents, err := src.Parse(path)
		if err != nil {
			return nil
		}
		for i := range ents {
			ents[i].Provider = source
		}
		return ents
	}
	return nil
}

// price resolves all three cost variants for every entry once at load time.
func price(entries []model.Entry) {
	table := pricing.Get()
	for i := range entries {
		e := &entries[i]
		rates, known := table.Find(e.Model)
		computed := rates.Cost(e.InputTokens, e.OutputTokens,
			e.CacheCreationTokens, e.CacheCreation1h, e.CacheReadTokens, e.SpeedFast)
		e.CostCalc = computed
		if e.ReportedCost != nil {
			e.CostDisplay = *e.ReportedCost
			e.CostAuto = *e.ReportedCost
		} else {
			e.CostDisplay = 0
			e.CostAuto = computed
		}
		if !known && e.TotalTokens() > 0 {
			e.MissingPricing = true
		}
	}
}

// DiscoverPaths is a small helper adapters may use for home-relative defaults.
func DiscoverPaths(parts ...string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	p := filepath.Join(append([]string{home}, parts...)...)
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return []string{p}, nil
	}
	return nil, nil
}
