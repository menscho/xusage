package augment

import (
	"os"
	"path/filepath"
	"strings"

	"xusage/internal/sources/jsonx"
)

// dataDirEnv overrides discovery entirely when set (comma-separated roots).
const dataDirEnv = "AUGMENT_DATA_DIR"

// sessionsSubdir is the directory under each data root that holds one JSON
// document per session.
const sessionsSubdir = "sessions"

// dataDirs returns the directories that may hold Augment session files.
// AUGMENT_DATA_DIR wins outright over the home-relative default, mirroring
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
	dir := filepath.Join(home, ".augment", sessionsSubdir)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return []string{dir}
	}
	return nil
}

// discover lists every .json file under every data root's sessions directory,
// sorted for deterministic loads. A missing data directory yields (nil, nil),
// never an error.
func discover() ([]string, error) {
	return jsonx.CollectFiles(dataDirs(), []string{".json"}, nil), nil
}
