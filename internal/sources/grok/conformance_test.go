package grok

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Upstream applies summary git_root_dir whenever info.cwd is absent — even
// when the path-derived project segment exists. The old code only consulted
// it when no sessions/<proj>/ segment had been parsed.
func TestGitRootDirFillsProjectWhenSummaryCwdAbsent(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	line := `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1}}}},"_meta":{"eventId":"evt-git"}}}`
	writeSession(t, root, "pathproj", "sess-git", line+"\n",
		`{"git_root_dir":"D:\\git\\repo"}`)
	entries := mustParse(t, filepath.Join(root, "sessions", "pathproj", "sess-git", "updates.jsonl"))
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Project != `D:\git\repo` {
		t.Errorf("Project = %q, want git_root_dir", entries[0].Project)
	}
}

// info.cwd still beats git_root_dir (upstream cwd.or(git_root_dir)).
func TestSummaryCwdStillBeatsGitRootDir(t *testing.T) {
	root := t.TempDir()
	withGrokHome(t, root)
	line := `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1}}}},"_meta":{"eventId":"evt-cwd"}}}`
	writeSession(t, root, "pathproj", "sess-cwd", line+"\n",
		`{"info":{"cwd":"D:\\real\\cwd"},"git_root_dir":"D:\\git\\repo"}`)
	entries := mustParse(t, filepath.Join(root, "sessions", "pathproj", "sess-cwd", "updates.jsonl"))
	if entries[0].Project != `D:\real\cwd` {
		t.Errorf("Project = %q, want info.cwd", entries[0].Project)
	}
}

// Upstream compares sessionUpdate with non_empty_string semantics (trimmed),
// but the quoted-substring prefilter still skips a padded value before the
// comparison runs — so the padded form yields no entry, exactly like upstream.
func TestSessionUpdateTrimmed(t *testing.T) {
	line := `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":" turn_completed ","usage":{"modelUsage":{"m":{"inputTokens":3,"outputTokens":1}}}},"_meta":{"eventId":"evt-pad"}}}`
	path := filepath.Join(t.TempDir(), "updates.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if entries := mustParse(t, path); len(entries) != 0 {
		t.Fatalf("padded value must be prefiltered out like upstream, got %d entries", len(entries))
	}
	// The untrimmed-but-quoted-free forms upstream never writes are not
	// special-cased either: the exact value passes both gates.
	exact := `{"timestamp":1750000000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"m":{"inputTokens":3,"outputTokens":1}}}},"_meta":{"eventId":"evt-exact"}}}`
	path = filepath.Join(t.TempDir(), "exact.jsonl")
	if err := os.WriteFile(path, []byte(exact+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if entries := mustParse(t, path); len(entries) != 1 {
		t.Fatalf("exact value must parse, got %d entries", len(entries))
	}
}

// Rows without eventId that differ only in reasoning stay distinct: the
// content dedup key carries reasoning like upstream dedupe_key.
func TestContentDedupKeyIncludesReasoning(t *testing.T) {
	mk := func(reasoning int) string {
		rec := map[string]any{
			"timestamp": 1750000000,
			"params": map[string]any{
				"sessionId": "sess-r",
				"update": map[string]any{
					"sessionUpdate": "turn_completed",
					"usage": map[string]any{
						"modelUsage": map[string]any{
							"m": map[string]any{
								"inputTokens": 10, "outputTokens": 2,
								"reasoningTokens": reasoning,
							},
						},
					},
				},
			},
		}
		data, _ := json.Marshal(rec)
		return string(data)
	}
	path := filepath.Join(t.TempDir(), "updates.jsonl")
	content := mk(1) + "\n" + mk(5) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := mustParse(t, path)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (reasoning distinguishes rows)", len(entries))
	}
}

// Identical rows without eventId still collapse.
func TestContentDedupStillCollapsesIdenticalRows(t *testing.T) {
	line := contentLine(10, 2, 0)
	path := filepath.Join(t.TempDir(), "updates.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if entries := mustParse(t, path); len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
}

// Timestamps may arrive as float-formatted JSON numbers; upstream
// value_as_i64 falls back to as_f64 truncation.
func TestTimestampFloatFormAccepted(t *testing.T) {
	line := `{"timestamp":1750000000.0,"params":{"update":{"sessionUpdate":"turn_completed",` +
		`"usage":{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1}}}},` +
		`"_meta":{"eventId":"evt-f","agentTimestampMs":1785328986355.0}}}`
	path := filepath.Join(t.TempDir(), "updates.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := mustParse(t, path)
	want := time.UnixMilli(1785328986355).UTC()
	if len(entries) != 1 || !entries[0].Timestamp.Equal(want) {
		got := time.Time{}
		if len(entries) == 1 {
			got = entries[0].Timestamp
		}
		t.Fatalf("Timestamp = %v, want %v", got, want)
	}
}

// Envelope timestamp in float seconds also parses (numI64 fallback).
func TestEnvelopeFloatSeconds(t *testing.T) {
	line := `{"timestamp":1750000000.0,"params":{"update":{"sessionUpdate":"turn_completed",` +
		`"usage":{"modelUsage":{"m":{"inputTokens":1,"outputTokens":1}}}},` +
		`"_meta":{"eventId":"evt-fs"}}}`
	path := filepath.Join(t.TempDir(), "updates.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := mustParse(t, path)
	if len(entries) != 1 || !entries[0].Timestamp.Equal(time.Unix(1750000000, 0).UTC()) {
		t.Fatalf("entries = %+v", entries)
	}
}
