package gemini

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/jsonx"
)

// dataDirEnv is the Gemini data-directory override.
const dataDirEnv = "GEMINI_DATA_DIR"

// dataDirs returns the Gemini roots to scan (the tmp/ directory in the default
// layout). GEMINI_DATA_DIR (comma-separated) wins over the home default,
// mirroring upstream paths().
func dataDirs() []string {
	var candidates []string
	if raw := strings.TrimSpace(os.Getenv(dataDirEnv)); raw != "" {
		candidates = append(candidates, jsonx.SplitEnv(raw)...)
	} else if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".gemini", "tmp"))
	}
	return jsonx.ExistingDirs(candidates)
}

// discoverLogFiles collects .json and .jsonl files under every data root.
func discoverLogFiles() []string {
	return jsonx.CollectFiles(dataDirs(), []string{".json", ".jsonl"}, nil)
}
