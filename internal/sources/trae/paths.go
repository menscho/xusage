package trae

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/jsonx"
)

// dataDirEnv overrides discovery entirely when set (comma-separated roots).
const dataDirEnv = "TRAE_DATA_DIR"

// dataDirs returns the directories that may hold Trae's usage cache.
// TRAE_DATA_DIR wins outright over the home-relative default, mirroring
// upstream paths(). Missing or non-directory entries are dropped silently —
// absence is normal.
func dataDirs() []string {
	if raw := strings.TrimSpace(os.Getenv(dataDirEnv)); raw != "" {
		return jsonx.ExistingDirs(jsonx.SplitEnv(raw))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	// XDG default cache home; falls back to ~/.cache when XDG_CACHE_HOME is
	// unset. Trae keeps its usage cache there.
	cache := filepath.Join(home, ".cache")
	if xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdg != "" {
		cache = xdg
	}
	dir := filepath.Join(cache, "trae")
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
