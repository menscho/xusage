// Discovery of Devin CLI session databases.
//
// Devin CLI persists per-session usage totals in a SQLite database
// (~/.local/share/devin/cli/sessions.db, see upstream rust/adapters/devin).
// Mirrors the discovery contract every source honors: an env override wins
// entirely over the home-relative default, only existing files count, and
// missing data yields (nil, nil).
package devin

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvDataDir overrides discovery entirely when set (comma-separated roots).
const EnvDataDir = "DEVIN_DATA_DIR"

// dbFileName is the upstream sessions database file name.
const dbFileName = "sessions.db"

// dataDirs returns the directories that may hold the Devin sessions database.
//
// DEVIN_DATA_DIR wins outright: its comma-separated entries are used as-is and
// the home-relative default is ignored, mirroring upstream paths(). Missing or
// non-directory entries are dropped silently — absence is normal.
func dataDirs() []string {
	if raw := os.Getenv(EnvDataDir); strings.TrimSpace(raw) != "" {
		return existingDirs(splitEnv(raw))
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return existingDirs([]string{filepath.Join(home, ".local", "share", "devin", "cli")})
}

// discover lists the sessions database under each known data dir, sorted for
// deterministic loads. A missing database is normal and yields (nil, nil), never
// an error.
func discover() ([]string, error) {
	var files []string
	seen := map[string]bool{}
	for _, dir := range dataDirs() {
		p := filepath.Join(dir, dbFileName)
		if seen[p] {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			seen[p] = true
			files = append(files, p)
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
