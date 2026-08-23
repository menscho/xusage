// Package amp adapts Amp thread files (~/.local/share/amp/threads/*.json)
// into xusage entries.
//
// A thread file is a single JSON object holding the thread id, its chat
// messages, and an optional usage ledger. When a ledger is present it is
// authoritative; otherwise per-message usage blocks are used. Discovery
// mirrors upstream: AMP_DATA_DIR wins over ~/.local/share/amp, and thread
// files live under the threads/ subdirectory.
package amp

import (
	"path/filepath"

	"xusage/internal/sources/adapter"
)

// dataDirs resolves the Amp data directories: AMP_DATA_DIR (comma-separated,
// env wins entirely) or the ~/.local/share/amp default.
func dataDirs() []string {
	return adapter.ExistingDirs(adapter.Env("AMP_DATA_DIR"),
		filepath.Join(adapter.HomeDir(), ".local", "share", "amp"))
}

// discover returns every thread .json file, or (nil, nil) when no data
// directory exists.
func discover() ([]string, error) {
	var files []string
	for _, dir := range dataDirs() {
		files = append(files, adapter.CollectFiles(filepath.Join(dir, "threads"), ".json")...)
	}
	adapter.SortStrings(files)
	return files, nil
}

// projectFor names the project column for amp entries; upstream hardcodes the
// agent name because threads carry no working directory.
func projectFor() string { return "amp" }
