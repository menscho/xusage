// Package codebuff adapts Codebuff usage data (chat-messages.json files under
// the Codebuff config directory) into normalized usage entries.
//
// Upstream reference: rust/adapters/codebuff/src/{paths.rs,parser.rs,loader.rs}
// in the ccusage repository. Discovery mirrors paths.rs: the CODEBUFF_DATA_DIR
// environment variable (comma-separated roots) wins over the per-channel
// defaults under the user's home directory, and only files named
// "chat-messages.json" inside "<root>/projects/<project>/chats/<chat>/" are
// considered usage data.
package codebuff

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// envDataDir is the environment variable overriding the default Codebuff data
// roots. It may hold several roots separated by the OS path list separator or
// by commas, matching the upstream implementation.
const envDataDir = "CODEBUFF_DATA_DIR"

// channels are the Codebuff config directories probed under the user's home
// directory when no explicit override is set.
var channels = []string{"manicode", "manicode-dev", "manicode-staging"}

// chatFileName is the only file name treated as Codebuff usage data.
const chatFileName = "chat-messages.json"

// dataRoots returns the configured Codebuff data roots: the environment
// override split on commas when set (upstream splits on ',' only), otherwise
// the per-channel defaults under the home directory.
func dataRoots() []string {
	if raw := os.Getenv(envDataDir); raw != "" {
		var roots []string
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				roots = append(roots, part)
			}
		}
		if len(roots) > 0 {
			return roots
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	roots := make([]string, 0, len(channels))
	for _, channel := range channels {
		roots = append(roots, filepath.Join(home, ".config", channel))
	}
	return roots
}

// projectRoots resolves each data root to its "projects" directory. A root
// whose base name is exactly "projects" is used as-is (upstream compares the
// file name exactly); otherwise "<root>/projects" is probed. Duplicates are
// dropped, and roots whose projects directory does not exist are skipped
// silently — a missing data directory is normal.
func projectRoots() []string {
	var roots []string
	seen := make(map[string]bool)
	for _, root := range dataRoots() {
		projects := root
		if filepath.Base(root) == "projects" {
			projects = root
		} else {
			projects = filepath.Join(root, "projects")
		}
		if seen[projects] {
			continue
		}
		if info, err := os.Stat(projects); err != nil || !info.IsDir() {
			continue
		}
		seen[projects] = true
		roots = append(roots, projects)
	}
	return roots
}

// DiscoverChatFiles returns the absolute paths of every chat-messages.json
// below the configured project roots, sorted for deterministic processing.
func DiscoverChatFiles() ([]string, error) {
	var files []string
	seen := make(map[string]bool)
	for _, projects := range projectRoots() {
		err := filepath.WalkDir(projects, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// Unreadable subtrees are skipped; discovery never fails hard.
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || d.Name() != chatFileName {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				abs = path
			}
			if !seen[abs] {
				seen[abs] = true
				files = append(files, abs)
			}
			return nil
		})
		if err != nil {
			// WalkDir only surfaces a top-level failure here; ignore and
			// continue with whatever other roots provide.
			continue
		}
	}
	sort.Strings(files)
	return files, nil
}
