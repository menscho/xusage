package kimi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// writeKimiTree materializes a kimi data root and points KIMI_DATA_DIR at it.
func writeKimiTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KIMI_DATA_DIR", root)
	return root
}

func TestDiscoverWireLayouts(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "old layout wire.jsonl only",
			files: map[string]string{
				"sessions/group/session-a/wire.jsonl":  "{}\n",
				"sessions/group/session-a/other.jsonl": "{}\n",
			},
			want: []string{"sessions/group/session-a/wire.jsonl"},
		},
		{
			name: "new layout with agents dir",
			files: map[string]string{
				"sessions/ws/session-b/agents/agent-1/wire.jsonl":  "{}\n",
				"sessions/group/session-c/wire.jsonl":              "{}\n",
				"sessions/ws/session-b/agents/agent-1/extra.jsonl": "{}\n",
			},
			want: []string{
				"sessions/group/session-c/wire.jsonl",
				"sessions/ws/session-b/agents/agent-1/wire.jsonl",
			},
		},
		{
			name: "wrong depth wire.jsonl ignored",
			files: map[string]string{
				"sessions/session-d/wire.jsonl":         "{}\n",
				"sessions/a/b/c/d/session-e/wire.jsonl": "{}\n",
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeKimiTree(t, tt.files)
			got, err := Discover()
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			var rels []string
			for _, p := range got {
				rel, err := filepath.Rel(root, p)
				if err != nil {
					t.Fatal(err)
				}
				rels = append(rels, filepath.ToSlash(rel))
			}
			if len(rels) != len(tt.want) {
				t.Fatalf("got %v, want %v", rels, tt.want)
			}
			for i := range tt.want {
				if rels[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", rels, tt.want)
				}
			}
		})
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	t.Setenv("KIMI_DATA_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := Discover()
	if err != nil {
		t.Fatalf("missing dir must not error, got %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("want no files, got %v", files)
	}
}

const statusLine = `{"timestamp":1770983427.123,"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":100,"output":50,"input_cache_read":10,"input_cache_creation":20},"message_id":"msg-1"}}}`

func TestParseOldFormat(t *testing.T) {
	dir := writeKimiTree(t, map[string]string{
		"config.json": `{"model":"kimi-k2"}`,
		"sessions/group/session-a/wire.jsonl": joinLines(
			`{"type":"metadata","protocol_version":"1.3"}`,
			`{"timestamp":1770983426.4,"message":{"type":"TurnBegin","payload":{"user_input":"hello"}}}`,
			statusLine,
		),
	})
	entries, err := Parse(filepath.Join(dir, "sessions", "group", "session-a", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.ID != "msg-1" || e.SessionID != "session-a" || e.Model != "kimi-k2" {
		t.Errorf("identity mismatch: %+v", e)
	}
	if e.InputTokens != 100 || e.OutputTokens != 50 ||
		e.CacheCreationTokens != 20 || e.CacheReadTokens != 10 {
		t.Errorf("token mapping mismatch: %+v", e)
	}
	if want := time.UnixMilli(1770983427123).UTC(); !e.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", e.Timestamp, want)
	}
	if e.Provider != "" {
		t.Errorf("adapter must leave Provider empty, got %q", e.Provider)
	}
}

func TestParseNewFormatTurnScopeOnly(t *testing.T) {
	dir := writeKimiTree(t, map[string]string{
		"sessions/ws/session-b/agents/agent-1/wire.jsonl": joinLines(
			`{"type":"usage.record","model":"kimi-code/kimi-for-coding","usage":{"inputOther":3064,"output":76,"inputCacheRead":14848,"inputCacheCreation":0},"usageScope":"turn","time":1782113184943}`,
			`{"type":"usage.record","model":"kimi-code/kimi-for-coding","usage":{"inputOther":5000,"output":200,"inputCacheRead":20000,"inputCacheCreation":100},"usageScope":"session","time":1782113185000}`,
		),
	})
	entries, err := Parse(filepath.Join(dir, "sessions", "ws", "session-b", "agents", "agent-1", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("session-scoped record must be skipped; got %d entries", len(entries))
	}
	e := entries[0]
	if e.Model != "kimi-for-coding" {
		t.Errorf("model = %q, want kimi-for-coding (kimi-code/ prefix stripped)", e.Model)
	}
	if e.InputTokens != 3064 || e.OutputTokens != 76 || e.CacheReadTokens != 14848 {
		t.Errorf("token mismatch: %+v", e)
	}
	if e.SessionID != "session-b" {
		t.Errorf("session = %q, want session-b (new layout resolves above agents/)", e.SessionID)
	}
}

func TestParseTotalOnlyBackfillsOutput(t *testing.T) {
	dir := writeKimiTree(t, map[string]string{
		"sessions/group/session-a/wire.jsonl": joinLines(
			`{"timestamp":1770983427.123,"message":{"type":"StatusUpdate","payload":{"token_usage":{"total":432}}}}`,
		),
	})
	entries, err := Parse(filepath.Join(dir, "sessions", "group", "session-a", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	// Upstream apply_total_token_fallback: with every split part absent the
	// grand total lands in OutputTokens, not ExtraTotalTokens.
	if entries[0].OutputTokens != 432 || entries[0].ExtraTotalTokens != 0 {
		t.Errorf("out=%d extra=%d, want 432/0", entries[0].OutputTokens, entries[0].ExtraTotalTokens)
	}
	if entries[0].TotalTokens() != 432 {
		t.Errorf("TotalTokens() = %d, want 432", entries[0].TotalTokens())
	}
}

// Upstream apply_total_token_fallback also runs when split parts exist: a total
// above the known buckets backfills zero output first, then spills to extra.
func TestParseTotalGapBackfillsOutputThenExtra(t *testing.T) {
	dir := writeKimiTree(t, map[string]string{
		"sessions/group/session-a/wire.jsonl": joinLines(
			`{"timestamp":1770983427.123,"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":100,"output":50,"input_cache_read":10,"input_cache_creation":20,"total":400},"message_id":"msg-gap"}}}`,
			`{"timestamp":1770983427.123,"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":100,"output":0,"total":300},"message_id":"msg-zero-out"}}}`,
		),
	})
	entries, err := Parse(filepath.Join(dir, "sessions", "group", "session-a", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}
	byID := map[string]model.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	gap := byID["msg-gap"]
	if gap.OutputTokens != 50 || gap.ExtraTotalTokens != 220 {
		t.Errorf("known-output row: out=%d extra=%d, want 50/220", gap.OutputTokens, gap.ExtraTotalTokens)
	}
	zero := byID["msg-zero-out"]
	if zero.OutputTokens != 200 || zero.ExtraTotalTokens != 0 {
		t.Errorf("zero-output row: out=%d extra=%d, want 200/0", zero.OutputTokens, zero.ExtraTotalTokens)
	}
}

func TestParseSkipsMalformedAndZero(t *testing.T) {
	dir := writeKimiTree(t, map[string]string{
		"sessions/group/session-a/wire.jsonl": joinLines(
			`not json at all`,
			`{"timestamp":1770983427,"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":0,"output":0,"input_cache_read":0,"input_cache_creation":0}}}}`,
			`{"timestamp":1770983427.5,"message":{"type":"StatusUpdate","payload":{"token_usage":{"output":7},"message_id":"msg-2"}}}`,
		),
	})
	entries, err := Parse(filepath.Join(dir, "sessions", "group", "session-a", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "msg-2" {
		t.Fatalf("want only msg-2 to survive, got %+v", entries)
	}
}

func TestParseMissingFileErrors(t *testing.T) {
	if _, err := Parse(filepath.Join(t.TempDir(), "nope", "wire.jsonl")); err == nil {
		t.Fatal("unreadable file must return an error")
	}
}

// Upstream reads message_id and time through non_empty_string / lenient_i64:
// an empty-string id is absent (the entry never dedups), and a negative time
// falls back to the file mtime instead of producing a pre-1970 stamp.
func TestParseLenientIDAndTime(t *testing.T) {
	dir := writeKimiTree(t, map[string]string{
		"sessions/group/session-a/wire.jsonl": joinLines(
			`{"timestamp":1770983427.123,"message":{"type":"StatusUpdate","payload":{"token_usage":{"output":5},"message_id":""}}}`,
			`{"time":-500,"message":{"type":"StatusUpdate","payload":{"token_usage":{"output":7},"message_id":"neg-time"}}}`,
		),
	})
	entries, err := Parse(filepath.Join(dir, "sessions", "group", "session-a", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].ID != "" {
		t.Errorf("empty message_id must yield no id, got %q", entries[0].ID)
	}
	// Negative time is absent upstream: fall back to the file mtime, never a
	// pre-1970 UnixMilli(-500) stamp.
	fi, err := os.Stat(filepath.Join(dir, "sessions", "group", "session-a", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if entries[1].Timestamp.Before(time.Unix(1_700_000_000, 0)) ||
		entries[1].Timestamp.After(fi.ModTime().Add(time.Minute)) {
		t.Errorf("negative time must fall back to the file mtime, got %v (mtime %v)",
			entries[1].Timestamp, fi.ModTime())
	}
}

func TestParseDedupRelevantFields(t *testing.T) {
	// Two StatusUpdates sharing a message id: the engine dedups by Provider+ID,
	// so the adapter must surface ID and stable token counts.
	dir := writeKimiTree(t, map[string]string{
		"sessions/group/session-a/wire.jsonl": joinLines(
			statusLine,
			`{"timestamp":1770983428.123,"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":100,"output":50,"input_cache_read":10,"input_cache_creation":20},"message_id":"msg-1"}}}`,
		),
	})
	entries, err := Parse(filepath.Join(dir, "sessions", "group", "session-a", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("adapter emits every valid entry; got %d", len(entries))
	}
	if entries[0].ID != entries[1].ID || entries[0].ID == "" {
		t.Fatalf("dup message ids must carry identical non-empty ID: %+v", entries)
	}
}

func TestRegistration(t *testing.T) {
	for _, s := range sources.All() {
		if s.Name == "kimi" {
			if s.Discover == nil || s.Parse == nil {
				t.Fatal("kimi registered with nil funcs")
			}
			return
		}
	}
	t.Fatal("kimi source not registered")
}

func TestPricingCandidates(t *testing.T) {
	before := pricingCandidateModels("kimi-for-coding", kimiForCodingCutoffMs-1)
	after := pricingCandidateModels("kimi-for-coding", kimiForCodingCutoffMs)
	if before[0] != "moonshot/kimi-k2.5" {
		t.Errorf("before cutoff = %v", before)
	}
	if after[0] != "moonshot/kimi-k2.6" {
		t.Errorf("at/after cutoff = %v", after)
	}
	other := pricingCandidateModels("kimi-k2", 0)
	if len(other) != 3 || other[0] != "moonshot/kimi-k2" || other[2] != "kimi-k2" {
		t.Errorf("non-default candidates = %v", other)
	}
}

// joinLines joins JSONL fragments with newlines, trailing newline included.
func joinLines(lines ...string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
