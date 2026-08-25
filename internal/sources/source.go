// Package sources defines the registry every usage-source adapter plugs into.
// Adapters live in sibling packages (internal/sources/<name>) and register
// themselves from init(); the binary links them by blank-importing
// internal/sources/all.
package sources

import (
	"sync"

	"xusage/internal/model"
)

// Source describes one coding-agent data source.
type Source struct {
	// Name is the stable identifier used in API responses and filters
	// (e.g. "claude", "codex").
	Name string
	// Discover returns absolute paths of usage data files. A missing data
	// directory is not an error: return (nil, nil).
	Discover func() ([]string, error)
	// Parse extracts usage entries from one file. Malformed lines are skipped;
	// only unreadable files warrant an error.
	Parse func(path string) ([]model.Entry, error)
}

var registered []Source

// Register adds a source to the registry. Later registrations with a duplicate
// Name replace the earlier one. Only called from adapter init() functions,
// which run single-threaded before main; the registry is immutable afterwards.
func Register(s Source) {
	for i := range registered {
		if registered[i].Name == s.Name {
			registered[i] = s
			return
		}
	}
	registered = append(registered, s)
}

// registry is the lazily-built, immutable, sorted view of registered.
// Built once on first use: Register finishes during init (before main) while
// All/Names are only called afterwards from the poller and HTTP handlers, so a
// single build is safe and needs no locking on the hot path. The slices are
// shared and must be treated as read-only by callers.
var (
	regOnce  sync.Once
	regAll   []Source
	regNames []string
)

func buildRegistry() {
	// Transfer ownership of the backing array: Register only runs during init
	// (before the first All/Names call), so once buildRegistry runs the
	// registration slice is never appended to again. Reusing its array here
	// avoids a fresh allocation + copy; nil'ing registered drops the only other
	// reference so the engine can release it once regAll is swept.
	regAll = registered
	registered = nil
	for i := 1; i < len(regAll); i++ {
		for j := i; j > 0 && regAll[j].Name < regAll[j-1].Name; j-- {
			regAll[j], regAll[j-1] = regAll[j-1], regAll[j]
		}
	}
	regNames = make([]string, len(regAll))
	for i, s := range regAll {
		regNames[i] = s.Name
	}
}

// All returns the registered sources sorted by name. The returned slice is
// shared across callers and must not be modified.
func All() []Source {
	regOnce.Do(buildRegistry)
	return regAll
}

// Names returns the registered source names in sorted order. The returned
// slice is shared across callers and must not be modified.
func Names() []string {
	regOnce.Do(buildRegistry)
	return regNames
}
