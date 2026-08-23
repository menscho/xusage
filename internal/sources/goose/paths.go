package goose

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dbFileName is the SQLite database goose persists sessions into.
const dbFileName = "sessions.db"

// pathRootEnv overrides the goose data root, mirroring upstream GOOSE_PATH_ROOT.
const pathRootEnv = "GOOSE_PATH_ROOT"

// Discover returns goose usage files: the canonical sessions database when
// present (reported for detection parity with upstream, even though its SQLite
// format needs a driver xusage does not ship) plus any JSONL/JSON session
// exports found under the sessions directories. A missing data directory is
// normal and never an error: the result is (nil, nil).
func Discover() ([]string, error) {
	if root := strings.TrimSpace(os.Getenv(pathRootEnv)); root != "" {
		return collect(filepath.Join(root, "data", "sessions")), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, nil
	}
	return discoverUnder(home), nil
}

// discoverUnder mirrors upstream default_goose_db_candidates: it probes the
// platform-specific goose data roots below home.
func discoverUnder(home string) []string {
	return collect(
		filepath.Join(home, ".local", "share", "goose", "sessions"),
		filepath.Join(home, "Library", "Application Support", "goose", "sessions"),
		filepath.Join(home, ".local", "share", "Block", "goose", "sessions"),
	)
}

// collect gathers the sessions database and any JSONL/JSON exports from the
// given roots, deduplicated and sorted for deterministic ordering.
func collect(roots ...string) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	for _, root := range roots {
		if db := filepath.Join(root, dbFileName); func() bool {
			_, err := os.Stat(db)
			return err == nil
		}() {
			add(db)
		}
		// Unreadable or absent subtrees are skipped quietly: absence of data
		// is never an error.
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".jsonl", ".json":
				add(p)
			}
			return nil
		})
	}
	sort.Strings(paths)
	return paths
}
