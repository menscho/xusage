package all

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xusage/internal/sources"
)

// TestParseNeverPanicsOnGarbage feeds every registered adapter a battery of
// malformed inputs — truncated JSON, wrong-typed fields, binary noise, huge
// lines, and an unreadable path. Per the sources contract, Parse must never
// panic; it may return entries or an error, but crashing is a defect.
func TestParseNeverPanicsOnGarbage(t *testing.T) {
	payloads := []string{
		``,
		"{",
		`{"type":`,
		`[1,2,3`,
		`"a string"`,
		`123`,
		`null`,
		`{"usage":null}`,
		`{"message":{"usage":{"input":"x","output":[],"cacheRead":{}}}}`,
		`{"tokens":"not-an-object","time":{"created":-9999999999999999999}}`,
		`{"attributes":"not-an-object","gen_ai.usage.input_tokens":1e400}`,
		"\x00\x01\x02\xff\xfe binary noise",
		`{"timestamp":1e999,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"inputTokens":-5}}}}`,
		strings.Repeat(`{"padding":"`, 200_000), // long line, unterminated
	}
	dir := t.TempDir()
	var paths []string
	for i, p := range payloads {
		name := filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('0'+i/26))+".jsonl")
		if err := os.WriteFile(name, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	paths = append(paths, filepath.Join(dir, "does-not-exist.jsonl"))

	for _, src := range sources.All() {
		t.Run(src.Name, func(t *testing.T) {
			for _, p := range paths {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("Parse(%q) panicked: %v", filepath.Base(p), r)
						}
					}()
					_, _ = src.Parse(p) // error or not: both are fine
				}()
			}
		})
	}
}
