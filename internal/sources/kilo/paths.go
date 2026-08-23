// Package kilo adapts Kilo Code usage data to the common source registry.
//
// Upstream ccusage reads Kilo's SQLite database (kilo.db). Pure-Go xusage
// cannot link a SQLite driver (stdlib only), so this adapter consumes the same
// message payloads as newline-delimited JSON instead: one JSON object per
// line, shaped like a Kilo message row's data column. See docs/NOTES.md.
package kilo

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvDataDir overrides discovery entirely when set (comma-separated roots).
const EnvDataDir = "KILO_DATA_DIR"

// dbFileName is the upstream database file name; kept so users can point
// KILO_DATA_DIR at any directory holding kilo-shaped records.
const dbFileName = "kilo.db"

// dataDirs returns the directories that may hold Kilo usage data.
//
// KILO_DATA_DIR wins outright: its comma-separated entries are used as-is and
// home-relative defaults are ignored, mirroring upstream paths(). Missing or
// non-directory entries are dropped silently — absence is normal.
func dataDirs() []string {
	if raw := os.Getenv(EnvDataDir); strings.TrimSpace(raw) != "" {
		var dirs []string
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if fi, err := os.Stat(part); err == nil && fi.IsDir() {
				dirs = append(dirs, part)
			}
		}
		return dirs
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dir := filepath.Join(home, ".local", "share", "kilo")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return []string{dir}
	}
	return nil
}

// discover lists every .jsonl file under the known data dirs, sorted for
// deterministic loads. A missing data directory yields (nil, nil), never an
// error.
func discover() ([]string, error) {
	dirs := dataDirs()
	if len(dirs) == 0 {
		return nil, nil
	}
	var files []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip, not fail
			}
			if d.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
				return nil
			}
			if !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err // WalkDir aborted (root vanished): surface it
		}
	}
	sortStrings(files)
	return files, nil
}

// sortStrings is a tiny local insertion sort keeping paths.go dependency-light.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
