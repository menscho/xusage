// Package droid adapts Factory Droid usage data: per-session settings files
// under ~/.factory/sessions (or $DROID_SESSIONS_DIR).
//
// Discovery mirrors upstream adapters/droid/src/paths.rs: the env override wins
// over the home-relative default, entries are comma-separated, and only
// directories that actually exist are scanned.
package droid

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/adapter"
)

// sessionsDirEnv is the Droid session-directory override.
const sessionsDirEnv = "DROID_SESSIONS_DIR"

// sessionPaths returns the root directories to scan for *.settings.json files,
// deduplicated and filtered to existing directories. A set (even empty)
// environment variable wins entirely over the home-relative default, mirroring
// upstream. Absence of every candidate is normal (Droid not installed): the
// result is empty, not an error.
func sessionPaths() []string {
	var raw []string
	if v, ok := os.LookupEnv(sessionsDirEnv); ok {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				raw = append(raw, p)
			}
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		raw = []string{filepath.Join(home, ".factory", "sessions")}
	}
	seen := make(map[string]bool, len(raw))
	var out []string
	for _, p := range raw {
		if seen[p] {
			continue
		}
		seen[p] = true
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// discoverSettingsFiles walks every session root recursively collecting
// *.settings.json, mirroring upstream collect_files_with_extension + suffix
// filter (nested archive directories are included).
func discoverSettingsFiles() []string {
	var files []string
	for _, root := range sessionPaths() {
		for _, p := range adapter.CollectFiles(root, ".json") {
			if strings.HasSuffix(p, ".settings.json") {
				files = append(files, p)
			}
		}
	}
	return files
}
