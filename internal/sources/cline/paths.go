// Package cline adapts Cline VS Code-extension session transcripts into
// xusage entries. Cline stores per-task transcripts as JSONL under its
// globalStorage tasks directory; this adapter reads those transcripts and maps
// their token buckets onto model.Entry.
package cline

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/jsonx"
)

// storageDirEnv overrides the directory holding Cline session transcripts,
// mirroring upstream ccusage's env-var override.
const storageDirEnv = "CLINE_HOME"

// dataDirs resolves the Cline transcript directories. CLINE_HOME (comma-
// separated) wins entirely over the ~/.cline default. Only existing
// directories count; a missing directory is normal, not an error.
func dataDirs() []string {
	var candidates []string
	if raw := strings.TrimSpace(os.Getenv(storageDirEnv)); raw != "" {
		candidates = append(candidates, jsonx.SplitEnv(raw)...)
	} else if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".cline", "tasks"))
	}
	return jsonx.ExistingDirs(candidates)
}

// discoverFiles collects every .jsonl transcript below the data roots, sorted
// for stable load order. A missing data directory yields nil.
func discoverFiles() []string {
	return jsonx.CollectFiles(dataDirs(), []string{".jsonl"}, nil)
}
