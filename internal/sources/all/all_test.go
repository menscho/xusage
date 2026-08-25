package all

import (
	"testing"

	"xusage/internal/sources"
)

func TestAllRegistered(t *testing.T) {
	want := []string{
		"amp", "augment", "claude", "cline", "codebuff", "codex", "copilot",
		"cursor", "devin", "droid", "gemini", "goose", "grok", "hermes", "kilo",
		"kimi", "openclaw", "opencode", "pi", "qwen", "roocode", "trae", "zed",
	}
	got := sources.Names()
	if len(got) != len(want) {
		t.Fatalf("registered = %v (%d), want %d", got, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered = %v, want %v", got, want)
		}
	}
}
