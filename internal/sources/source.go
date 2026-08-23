// Package sources defines the registry every usage-source adapter plugs into.
// Adapters live in sibling packages (internal/sources/<name>) and register
// themselves from init(); the binary links them by blank-importing
// internal/sources/all.
package sources

import "xusage/internal/model"

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
// Name replace the earlier one.
func Register(s Source) {
	for i := range registered {
		if registered[i].Name == s.Name {
			registered[i] = s
			return
		}
	}
	registered = append(registered, s)
}

// All returns the registered sources sorted by name.
func All() []Source {
	out := make([]Source, len(registered))
	copy(out, registered)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Names returns the registered source names in sorted order.
func Names() []string {
	all := All()
	names := make([]string, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	return names
}
