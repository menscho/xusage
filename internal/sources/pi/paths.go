// Package pi adapts the pi coding agent's session logs (pi-agent).
//
// Data lives under $PI_AGENT_DIR when set (a comma-separated list of existing
// directories, deliberately without ~ expansion, mirroring upstream), otherwise
// the home-relative default ~/.pi/agent/sessions. Session files are .jsonl
// records collected recursively.
package pi

import (
	"os"
	"path/filepath"
	"strings"
)

// piAgentDirEnv mirrors upstream PI_AGENT_DIR.
const piAgentDirEnv = "PI_AGENT_DIR"

// sessionDirs returns the session root directories in priority order: the
// PI_AGENT_DIR list (existing dirs only, deduplicated) wins over the home
// default. A missing default directory is not an error.
func sessionDirs() []string {
	if raw := strings.TrimSpace(os.Getenv(piAgentDirEnv)); raw != "" {
		return existingDirs(raw)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	p := filepath.Join(home, ".pi", "agent", "sessions")
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return []string{p}
	}
	return nil
}

// existingDirs splits a comma-separated path list and keeps existing,
// deduplicated directories.
func existingDirs(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if fi, err := os.Stat(part); err != nil || !fi.IsDir() {
			continue
		}
		if seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

// discoverJSONL walks every session directory and collects .jsonl file paths.
// Directories that cannot be read are skipped silently.
func discoverJSONL(dirs []string) []string {
	var out []string
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".jsonl") {
				out = append(out, path)
			}
			return nil
		})
	}
	return out
}
