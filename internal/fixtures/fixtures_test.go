package fixtures

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClaudeLineDeterministic(t *testing.T) {
	o := ClaudeLineOpts{
		Seed: 1, ID: "msg_1", RequestID: "req_1", SessionID: "s",
		Model: "m", Input: 10, Output: 5,
	}
	a := ClaudeLine(o)
	b := ClaudeLine(o)
	if a != b {
		t.Fatalf("same opts produced different bytes:\n%s\n%s", a, b)
	}
	for _, want := range []string{`"id":"msg_1"`, `"model":"m"`, `"input_tokens":10`} {
		if !strings.Contains(a, want) {
			t.Errorf("line missing %s: %s", want, a)
		}
	}
}

func TestClaudeLineVariants(t *testing.T) {
	tests := []struct {
		name string
		opts ClaudeLineOpts
		want string
	}{
		{"fast speed", ClaudeLineOpts{SpeedFast: true}, `"speed":"fast"`},
		{"standard speed", ClaudeLineOpts{SpeedStandard: true}, `"speed":"standard"`},
		{"cache split 1h", ClaudeLineOpts{Cache5m: 5, Cache1h: 7},
			`"ephemeral_5m_input_tokens":5`},
		{"cache split 1h value", ClaudeLineOpts{Cache5m: 5, Cache1h: 7},
			`"ephemeral_1h_input_tokens":7`},
		{"flat cache", ClaudeLineOpts{Cache5m: 9}, `"cache_creation_input_tokens":9`},
		{"costUSD", ClaudeLineOpts{CostUSD: MustCost(0.25)}, `"costUSD":0.25`},
		{"no costUSD omits field", ClaudeLineOpts{}, `"costUSD"`},
		{"synthetic", ClaudeLineOpts{Synthetic: true}, `"<synthetic>"`},
		{"bad version", ClaudeLineOpts{BadVersion: "not-semver"}, `"version":"not-semver"`},
		{"sidechain", ClaudeLineOpts{Sidechain: true}, `"isSidechain":true`},
		{"null top-level field", ClaudeLineOpts{NullField: "sessionId"}, `"sessionId":null`},
		{"null usage field", ClaudeLineOpts{NullUsageField: "cache_read_input_tokens"},
			`"cache_read_input_tokens":null`},
		{"api error", ClaudeLineOpts{APIError: true}, `"isApiErrorMessage":true`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClaudeLine(tt.opts)
			has := strings.Contains(got, tt.want)
			if tt.name == "no costUSD omits field" {
				has = !has // want absence
			}
			if !has {
				t.Fatalf("line missing %q:\n%s", tt.want, got)
			}
		})
	}
}

func TestClaudeLineLimitResetMarker(t *testing.T) {
	reset := time.Date(2025, 6, 2, 13, 0, 0, 0, time.UTC)
	line := ClaudeLine(ClaudeLineOpts{APIError: true, LimitReset: reset})
	want := "Claude AI usage limit reached|" + strconv.FormatInt(reset.Unix(), 10)
	if !strings.Contains(line, want) {
		t.Fatalf("line missing limit-reset marker %q:\n%s", want, line)
	}
}

func TestClaudeLineNoUsage(t *testing.T) {
	line := ClaudeLine(ClaudeLineOpts{NoUsage: true})
	if strings.Contains(line, `"usage"`) || strings.Contains(line, `"id"`) {
		t.Fatalf("NoUsage line should carry neither usage nor message.id:\n%s", line)
	}
}

func TestClaudeLineMalformed(t *testing.T) {
	s := ClaudeLine(ClaudeLineOpts{Malformed: true})
	if !strings.HasSuffix(s, "\n") {
		t.Fatal("malformed line must stay newline-terminated so it cannot swallow the next record")
	}
	if !strings.Contains(s, `"usage"`) {
		t.Fatalf("malformed line should still look like truncated JSON, got %q", s)
	}
}

func TestCodexLineTokenCount(t *testing.T) {
	tok := CodexLine(CodexLineOpts{
		Input: 100, Cached: 40, Output: 20, Reasoning: 5,
	})
	for _, want := range []string{
		`"type":"event_msg"`, `"total_token_usage"`,
		`"cached_input_tokens":40`, `"reasoning_output_tokens":5`,
		`"total_tokens":165`,
	} {
		if !strings.Contains(tok, want) {
			t.Errorf("token line missing %s: %s", want, tok)
		}
	}
	last := CodexLine(CodexLineOpts{UseLast: true})
	if !strings.Contains(last, `"last_token_usage"`) {
		t.Fatalf("missing last_token_usage: %s", last)
	}
	totalOnly := CodexLine(CodexLineOpts{TotalOnly: true, Total: 900})
	if !strings.Contains(totalOnly, `"total_token_usage":{"total_tokens":900}`) ||
		strings.Contains(totalOnly, "input_tokens\":") {
		t.Fatalf("TotalOnly should emit a lone total: %s", totalOnly)
	}
	meta := CodexLine(CodexLineOpts{LineType: "session_meta"})
	if !strings.Contains(meta, `"type":"session_meta"`) {
		t.Fatalf("bad meta line: %s", meta)
	}
	turn := CodexLine(CodexLineOpts{LineType: "turn_context", Model: "gpt-5.3"})
	if !strings.Contains(turn, `"model":"gpt-5.3"`) {
		t.Fatalf("bad turn_context line: %s", turn)
	}
}

func TestSessionMeta(t *testing.T) {
	meta := SessionMeta(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "sess-1", "/c")
	if !strings.Contains(meta, `"type":"session_meta"`) ||
		!strings.Contains(meta, `"cwd":"/c"`) ||
		!strings.Contains(meta, `"id":"sess-1"`) {
		t.Fatalf("bad meta line: %s", meta)
	}
}

func TestMustCost(t *testing.T) {
	p := MustCost(1.5)
	if *p != 1.5 {
		t.Fatalf("MustCost returned %v", *p)
	}
}

func TestClaudeLineStableAcrossTempDirs(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	write := func(dir string) string {
		path := filepath.Join(dir, "line.jsonl")
		line := ClaudeLine(ClaudeLineOpts{Seed: 99})
		if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if write(dirA) != write(dirB) {
		t.Fatal("identical seeds produced different bytes")
	}
}
