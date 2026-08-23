// Package opencode adapts OpenCode's local usage storage to xusage.
//
// Layout mirrored from upstream ccusage (rust/adapters/opencode):
//
//   - data roots: $OPENCODE_DATA_DIR (comma-separated; when set it wins
//     entirely) or ~/.local/share/opencode
//   - one JSON message document per file under storage/message/**/*.json;
//     real installs store them pretty-printed, which parses identically
//   - a legacy SQLite database (opencode.db / opencode-*.db) holding the same
//     message payloads; it is not readable with the standard library alone,
//     so this adapter covers the JSON storage subset only
package opencode

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// envDataDir selects OpenCode data roots directly, overriding the default.
const envDataDir = "OPENCODE_DATA_DIR"

// defaultRoot is the home-relative storage location used when the environment
// variable is unset.
const defaultRoot = ".local/share/opencode"

// projectLabel is the project name upstream reports for every OpenCode entry;
// OpenCode storage does not carry a per-project path.
const projectLabel = "opencode"

// dataRoots returns the candidate data directories. The environment variable
// wins entirely: when it is set, the home-relative default is never consulted,
// mirroring upstream. Entries are deduplicated and missing directories are
// filtered out; absence is normal.
func dataRoots() []string {
	var raw []string
	if v := os.Getenv(envDataDir); strings.TrimSpace(v) != "" {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				raw = append(raw, part)
			}
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		raw = []string{filepath.Join(home, filepath.FromSlash(defaultRoot))}
	}
	seen := make(map[string]bool, len(raw))
	var roots []string
	for _, p := range raw {
		if seen[p] {
			continue
		}
		seen[p] = true
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			roots = append(roots, p)
		}
	}
	return roots
}

// discover collects every .json message file below storage/message in each
// data root. A missing or empty tree yields no files and no error.
func discover() ([]string, error) {
	var files []string
	for _, root := range dataRoots() {
		msgDir := filepath.Join(root, "storage", "message")
		if fi, err := os.Stat(msgDir); err != nil || !fi.IsDir() {
			continue
		}
		err := filepath.WalkDir(msgDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip, never fail discovery
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".json") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}
