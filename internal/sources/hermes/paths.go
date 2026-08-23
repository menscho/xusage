// Discovery of Hermes state databases.
//
// Mirrors upstream rust/adapters/hermes/src/paths.rs: the HERMES_HOME
// environment variable (a comma-separated list) overrides the default
// ~/.hermes directory; each home contributes a state.db file that must exist.
package hermes

import (
	"os"
	"path/filepath"
	"strings"
)

// hermesHomeEnv is the environment variable overriding the default home dir.
const hermesHomeEnv = "HERMES_HOME"

// stateDBPaths returns every existing state.db path in preference order:
// HERMES_HOME entries first (env wins), then the home-relative default.
// A missing home or database is normal and yields an empty slice.
func stateDBPaths(home func() (string, error)) []string {
	var homes []string
	if raw := strings.TrimSpace(os.Getenv(hermesHomeEnv)); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				homes = append(homes, p)
			}
		}
	} else if h, err := home(); err == nil && h != "" {
		homes = append(homes, filepath.Join(h, ".hermes"))
	}
	var out []string
	seen := map[string]bool{}
	for _, h := range homes {
		p := filepath.Join(h, "state.db")
		if seen[p] {
			continue
		}
		seen[p] = true
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}
