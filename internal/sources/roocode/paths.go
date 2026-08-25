// Discovery of Roo Code (roo-cline) task transcripts.
//
// Roo Code persists one JSONL transcript per task in VS Code's globalStorage
// (the roo-cline extension namespace). Mirrors the discovery contract every
// source honors: an env override wins entirely over the home-relative default,
// only existing directories count, and missing data yields (nil, nil).
package roocode

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvDataDir overrides discovery entirely when set (comma-separated roots).
const EnvDataDir = "ROOCODE_DATA_DIR"

// extNamespace is the roo-cline extension's VS Code globalStorage folder.
const extNamespace = "rooveterinaryinc.roo-cline"

// dataDirs returns the directories that may hold Roo Code task transcripts.
//
// ROOCODE_DATA_DIR wins outright: its comma-separated entries are used as-is
// and the home-relative default is ignored, mirroring upstream paths().
// Missing or non-directory entries are dropped silently — absence is normal.
func dataDirs() []string {
	if raw := os.Getenv(EnvDataDir); strings.TrimSpace(raw) != "" {
		return existingDirs(splitEnv(raw))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return existingDirs(globalStorageCandidates(home))
}

// globalStorageCandidates returns the platform-specific VS Code globalStorage
// roots that may hold the roo-cline task transcripts, each suffixed with the
// extension namespace and the tasks subdirectory.
func globalStorageCandidates(home string) []string {
	return []string{
		// Windows: %APPDATA%\Code\User\globalStorage\<ns>\tasks
		filepath.Join(home, "AppData", "Roaming", "Code", "User", "globalStorage", extNamespace, "tasks"),
		// macOS: ~/Library/Application Support/Code/User/globalStorage/<ns>/tasks
		filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", extNamespace, "tasks"),
		// Linux: ~/.config/Code/User/globalStorage/<ns>/tasks
		filepath.Join(home, ".config", "Code", "User", "globalStorage", extNamespace, "tasks"),
	}
}

// discover lists every .jsonl transcript under the known data dirs, sorted for
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

// existingDirs keeps only the entries that are existing directories,
// deduplicated in order.
func existingDirs(candidates []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// splitEnv splits a comma-separated value, trimming and dropping empties.
func splitEnv(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
