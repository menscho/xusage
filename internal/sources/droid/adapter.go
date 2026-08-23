// Package droid — registry wiring.
package droid

import (
	"sort"

	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "droid",
		Discover: discover,
		Parse:    parse,
	})
}

// discover returns every Droid settings file, sorted for deterministic loads.
// A missing data directory is not an error.
func discover() ([]string, error) {
	files := discoverSettingsFiles()
	sort.Strings(files)
	return files, nil
}

// parse extracts usage entries from one settings file. Per-file isolation in
// the loader turns any returned error into "zero entries", so a malformed or
// unreadable settings file never fails the whole load.
func parse(path string) ([]model.Entry, error) {
	return loadSettingsFile(path)
}
