package all

import (
	"os"
	"path/filepath"
	"testing"

	"xusage/internal/sources"
)

// TestDiscoverHermeticEmptyHome pins HOME/USERPROFILE at an empty temp dir and
// clears every per-adapter override env var. On such a machine no source may
// report data: Discover must return no files (nil or empty) and never an error.
func TestDiscoverHermeticEmptyHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, env := range []string{
		"PI_AGENT_DIR", "GOOSE_PATH_ROOT", "OPENCLAW_DIR", "KILO_DATA_DIR",
		"KIMI_DATA_DIR", "QWEN_DATA_DIR", "GEMINI_DATA_DIR", "GROK_HOME",
		"COPILOT_OTEL_FILE_EXPORTER_PATH", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
		"OPENCODE_DATA_DIR", "AMP_DIR", "CODEBUFF_DB_PATH", "DROID_SESSIONS_DIR",
		"HERMES_HOME",
	} {
		t.Setenv(env, "")
	}

	for _, src := range sources.All() {
		t.Run(src.Name, func(t *testing.T) {
			files, err := src.Discover()
			if err != nil {
				t.Fatalf("Discover() error = %v, want nil on an empty home", err)
			}
			if len(files) != 0 {
				t.Errorf("Discover() = %d files under %s, want none; first: %s",
					len(files), home, files[0])
			}
			for _, f := range files {
				if !filepath.IsAbs(f) {
					t.Errorf("path %q is not absolute", f)
				}
				if _, err := os.Stat(f); err != nil {
					t.Errorf("discovered path %q does not exist: %v", f, err)
				}
			}
		})
	}
}
