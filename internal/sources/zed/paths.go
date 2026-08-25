package zed

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/jsonx"
)

// dataDirEnv overrides discovery entirely when set (comma-separated roots).
const dataDirEnv = "ZED_DATA_DIR"

// dataDirs returns the directories that may hold Zed thread data. The native
// store is a SQLite database (threads.db); what this adapter reads is any
// JSONL/JSON export dropped alongside it. ZED_DATA_DIR wins outright over the
// home-relative default, mirroring upstream paths(). Missing or non-directory
// entries are dropped silently — absence is normal.
func dataDirs() []string {
	if raw := strings.TrimSpace(os.Getenv(dataDirEnv)); raw != "" {
		return jsonx.ExistingDirs(jsonx.SplitEnv(raw))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dir := filepath.Join(home, ".local", "share", "zed", "threads")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return []string{dir}
	}
	return nil
}

// discover lists every .jsonl/.json file under every data root, sorted for
// deterministic loads. A missing data directory yields (nil, nil), never an
// error.
func discover() ([]string, error) {
	return jsonx.CollectFiles(dataDirs(), []string{".jsonl", ".json"}, nil), nil
}
