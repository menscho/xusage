// Package grok adapts the Grok CLI's local session logs into xusage usage
// entries. Data lives under $GROK_HOME (or ~/.grok) as one updates.jsonl per
// session directory, optionally accompanied by a summary.json carrying the
// canonical session id, working directory, and default model.
package grok

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// grokHomeEnv overrides the Grok CLI data root.
const grokHomeEnv = "GROK_HOME"

// resolveRoot returns the Grok data root: $GROK_HOME when it names an existing
// directory, otherwise ~/.grok when that exists. The second result is false
// when no root directory could be located, which is normal on machines
// without Grok CLI data.
func resolveRoot() (string, bool) {
	if home := strings.TrimSpace(os.Getenv(grokHomeEnv)); home != "" {
		if fi, err := os.Stat(home); err == nil && fi.IsDir() {
			return home, true
		}
		return "", false
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	p := filepath.Join(userHome, ".grok")
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p, true
	}
	return "", false
}

// discoverSessionFiles returns the absolute path of every
// sessions/**/updates.jsonl under the resolved root, sorted for stable load
// order. A missing or unreadable tree is not an error: it yields no files.
func discoverSessionFiles() ([]string, error) {
	root, ok := resolveRoot()
	if !ok {
		return nil, nil
	}
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "sessions"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip it, never fail discovery
		}
		if !d.IsDir() && d.Name() == "updates.jsonl" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil
	}
	sort.Strings(files)
	return files, nil
}
