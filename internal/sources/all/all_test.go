package all

import (
	"testing"

	"xusage/internal/sources"
)

func TestAllSixteenRegistered(t *testing.T) {
	want := []string{
		"amp", "claude", "codebuff", "codex", "copilot", "droid", "gemini",
		"goose", "grok", "hermes", "kilo", "kimi", "openclaw", "opencode",
		"pi", "qwen",
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
