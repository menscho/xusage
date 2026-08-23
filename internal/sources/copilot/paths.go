package copilot

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/jsonx"
)

// exporterPathEnv overrides the default ~/.copilot/otel directory with an
// exact file path, mirroring upstream COPILOT_OTEL_FILE_EXPORTER_PATH.
const exporterPathEnv = "COPILOT_OTEL_FILE_EXPORTER_PATH"

// discoverFiles returns the OTel JSONL files to parse: everything under the
// default directory plus the env-named exporter file, deduplicated and sorted.
// Absence of either is normal.
func discoverFiles() []string {
	var files []string
	if home, err := os.UserHomeDir(); err == nil {
		otelDir := filepath.Join(home, ".copilot", "otel")
		if fi, err := os.Stat(otelDir); err == nil && fi.IsDir() {
			files = append(files, jsonx.CollectFiles([]string{otelDir}, []string{".jsonl"}, nil)...)
		}
	}
	if p := strings.TrimSpace(os.Getenv(exporterPathEnv)); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			files = append(files, p)
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
