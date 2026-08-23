// Package openclaw adapts OpenClaw session JSONL logs into xusage entries.
//
// OpenClaw (formerly Clawdbot / Moltbot / Moldbot) writes one JSONL file per
// session below its data directory. Discovery mirrors the upstream adapter:
// the OPENCLAW_DIR environment variable (comma-separated list) overrides the
// ~/.openclaw, ~/.clawdbot, ~/.moltbot and ~/.moldbot home defaults, and every
// *.jsonl file — including archived *.jsonl.deleted.* / *.jsonl.reset.* forms
// — below a data root is a session file.
package openclaw

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// envDir overrides the default data roots when set.
const envDir = "OPENCLAW_DIR"

// defaultRoots lists the home-relative data directories in upstream order.
func defaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".openclaw"),
		filepath.Join(home, ".clawdbot"),
		filepath.Join(home, ".moltbot"),
		filepath.Join(home, ".moldbot"),
	}
}

// discoverDirs resolves the data roots to walk. OPENCLAW_DIR (comma-separated,
// deduplicated) wins over the home defaults; only existing directories are
// returned. A missing data directory is normal, not an error.
func discoverDirs() []string {
	if raw := strings.TrimSpace(os.Getenv(envDir)); raw != "" {
		var dirs []string
		seen := map[string]bool{}
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			if fi, err := os.Stat(part); err != nil || !fi.IsDir() {
				continue
			}
			seen[part] = true
			dirs = append(dirs, part)
		}
		return dirs
	}
	var dirs []string
	for _, dir := range defaultRoots() {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// isSessionFile reports whether name looks like an OpenClaw session log:
// "*.jsonl" plus the archived "*.jsonl.deleted.*" / "*.jsonl.reset.*" forms.
func isSessionFile(name string) bool {
	i := strings.Index(name, ".jsonl")
	if i < 0 {
		return false
	}
	suffix := name[i:]
	return suffix == ".jsonl" ||
		strings.HasPrefix(suffix, ".jsonl.deleted.") ||
		strings.HasPrefix(suffix, ".jsonl.reset.")
}

// sessionFilesIn recursively collects session files under root, sorted.
// Symlinks are skipped, mirroring upstream collect_session_files.
func sessionFilesIn(root string) ([]string, error) {
	// WalkDir does not follow a symlinked root; resolve it so a symlinked
	// data directory is still walked, like Rust's read_dir would.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && isSessionFile(d.Name()) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// extractSessionID derives the session id from the file name: the stem before
// the first ".jsonl" occurrence, or the whole name when the stem is empty.
func extractSessionID(path string) string {
	name := filepath.Base(path)
	i := strings.Index(name, ".jsonl")
	if i <= 0 {
		return name
	}
	return name[:i]
}
