// Package cursor adapts Cursor IDE usage-export transcripts into xusage
// entries. Cursor can export its usage API data as JSONL (one JSON object per
// line); this adapter reads those objects and maps their token buckets onto
// model.Entry.
package cursor

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/jsonx"
)

// usageDirEnv overrides the directory holding Cursor usage exports,
// mirroring upstream ccusage's env-var override.
const usageDirEnv = "CURSOR_HOME"

// dataDirs resolves the Cursor usage-export directories. CURSOR_HOME
// (comma-separated) wins entirely over the ~/.cursor/usage default. Only
// existing directories count; a missing directory is normal, not an error.
func dataDirs() []string {
	var candidates []string
	if raw := strings.TrimSpace(os.Getenv(usageDirEnv)); raw != "" {
		candidates = append(candidates, jsonx.SplitEnv(raw)...)
	} else if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".cursor", "usage"))
	}
	return jsonx.ExistingDirs(candidates)
}

// discoverFiles collects every .jsonl usage export below the data roots,
// sorted for stable load order. A missing data directory yields nil.
func discoverFiles() []string {
	return jsonx.CollectFiles(dataDirs(), []string{".jsonl"}, nil)
}
