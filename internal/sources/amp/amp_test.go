package amp

import (
	"os"
	"path/filepath"
	"testing"

	"xusage/internal/model"
)

func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseLedgerHappyPath(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "threads/t1.json", `{
		"id": "thread-1",
		"messages": [
			{"role": "assistant", "messageId": 7, "usage": {"inputTokens": 1, "outputTokens": 1,
				"cacheCreationInputTokens": 5, "cacheReadInputTokens": 10}}
		],
		"usageLedger": {"events": [
			{"id": "ev-1", "timestamp": "2026-03-15T10:00:00Z", "model": "claude-sonnet-4-20250514",
			 "to_message_id": 7, "tokens": {"input": 100, "output": 50, "total": 165}},
			{"id": "ev-2", "timestamp": "2026-03-15T10:01:00Z", "model": "gpt-5.2",
			 "tokens": {"input": 0, "output": 0, "total": 321}}
		]}
	}`)
	entries, err := parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.SessionID != "thread-1" || e.Project != "amp" {
		t.Errorf("session/project wrong: %+v", e)
	}
	// Cache buckets join from message 7's usage via to_message_id (upstream);
	// ledger tokens.cache is ignored.
	if e.InputTokens != 100 || e.OutputTokens != 50 || e.CacheReadTokens != 10 || e.CacheCreationTokens != 5 {
		t.Errorf("token split wrong: %+v", e)
	}
	if e.ExtraTotalTokens != 0 {
		t.Errorf("covered total should not spill to extra: %d", e.ExtraTotalTokens)
	}
	// Total-only ledger event backfills output (upstream fallback), extra stays 0.
	if entries[1].OutputTokens != 321 || entries[1].ExtraTotalTokens != 0 {
		t.Errorf("total-only event should backfill output: %+v", entries[1])
	}
}

func TestParseMessagesFallback(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "threads/t2.json", `{
		"id": "thread-2",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "messageId": 42, "timestamp": "2026-03-15T11:00:00Z",
			 "model": "claude-opus-4-20250514",
			 "usage": {"timestamp": "2026-03-15T11:00:00Z", "inputTokens": 200, "outputTokens": 80,
			           "cacheReadInputTokens": 30, "cacheCreationInputTokens": 20}}
		]
	}`)
	entries, err := parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.ID != "42" {
		t.Errorf("numeric messageId should stringify: %q", e.ID)
	}
	if e.InputTokens != 200 || e.OutputTokens != 80 || e.CacheReadTokens != 30 || e.CacheCreationTokens != 20 {
		t.Errorf("usage mapping wrong: %+v", e)
	}
}

func TestParseMalformedAndEmpty(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "threads/bad.json", `{not json`)
	if entries, err := parse(bad); err != nil || len(entries) != 0 {
		t.Errorf("malformed thread should yield no entries and no error: %v %v", entries, err)
	}
	noid := writeFile(t, dir, "threads/noid.json", `{"messages":[]}`)
	if entries, err := parse(noid); err != nil || len(entries) != 0 {
		t.Errorf("id-less thread should yield nothing: %v %v", entries, err)
	}
	if _, err := parse(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("unreadable file should error")
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	t.Setenv("AMP_DATA_DIR", filepath.Join(t.TempDir(), "nope"))
	files, err := discover()
	if err != nil {
		t.Fatal(err)
	}
	if files != nil && len(files) != 0 {
		t.Errorf("missing dir must be empty, got %v", files)
	}
}

func TestDedupRelevantFieldsPresent(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "threads/t3.json", `{
		"id": "thread-3",
		"messages": [],
		"usageLedger": {"events": [
			{"id": "same-id", "timestamp": "2026-03-15T10:00:00Z", "model": "m",
			 "tokens": {"input": 10, "output": 5}},
			{"id": "same-id", "timestamp": "2026-03-15T10:00:00Z", "model": "m",
			 "tokens": {"input": 10, "output": 5}}
		]}
	}`)
	entries, err := parse(file)
	if err != nil {
		t.Fatal(err)
	}
	// The adapter emits every valid entry; dedup happens in the loader. Both
	// rows must carry the identical ID so the loader can collapse them.
	if len(entries) != 2 || entries[0].ID != entries[1].ID || entries[0].ID == "" {
		t.Errorf("dup IDs must survive the adapter for loader dedup: %+v", entries)
	}
	var _ model.Entry // keep model import honest
}

func TestTotalFallbackBackfillsOutput(t *testing.T) {
	// Upstream apply_total_token_fallback: a lone total backfills zero output
	// instead of being dropped (both ledger and messages paths).
	tests := []struct {
		name            string
		thread          string
		wantIn, wantOut uint64
		wantCC, wantCR  uint64
		wantExtra       uint64
	}{
		{
			name: "messages path total-only",
			thread: `{"id":"T-a","messages":[{"role":"assistant","usage":{
				"model":"m","totalTokens":345,"timestamp":"2026-01-19T11:42:10.652Z"}}]}`,
			wantOut: 345,
		},
		{
			name: "messages path remainder becomes extra",
			thread: `{"id":"T-a","messages":[{"role":"assistant","usage":{
				"model":"m","inputTokens":10,"outputTokens":5,"cacheReadInputTokens":25,
				"totalTokens":60,"timestamp":"2026-01-19T11:42:10.652Z"}}]}`,
			wantIn: 10, wantOut: 5, wantCR: 25, wantExtra: 20,
		},
		{
			name: "ledger remainder becomes extra",
			thread: `{"id":"T-a","usageLedger":{"events":[{"id":"e1",
				"timestamp":"2026-01-02T00:00:00.000Z","model":"m",
				"tokens":{"input":10,"output":5,"total":60}}]}}`,
			wantIn: 10, wantOut: 5, wantExtra: 45,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := writeFile(t, t.TempDir(), "threads/x.json", tt.thread)
			entries, err := parse(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			e := entries[0]
			if e.InputTokens != tt.wantIn || e.OutputTokens != tt.wantOut ||
				e.CacheCreationTokens != tt.wantCC || e.CacheReadTokens != tt.wantCR ||
				e.ExtraTotalTokens != tt.wantExtra {
				t.Errorf("got in=%d out=%d cc=%d cr=%d extra=%d, want in=%d out=%d cc=%d cr=%d extra=%d",
					e.InputTokens, e.OutputTokens, e.CacheCreationTokens, e.CacheReadTokens,
					e.ExtraTotalTokens, tt.wantIn, tt.wantOut, tt.wantCC, tt.wantCR, tt.wantExtra)
			}
		})
	}
}

func TestEmptyLedgerFallsBackToMessages(t *testing.T) {
	// A usageLedger without a usable events array must not win precedence.
	file := writeFile(t, t.TempDir(), "threads/t.json", `{
		"id": "T-empty",
		"usageLedger": {},
		"messages": [{"role": "assistant", "usage": {
			"model": "m", "inputTokens": 10, "outputTokens": 178,
			"timestamp": "2026-01-19T11:42:10.652Z"}}]
	}`)
	entries, err := parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].InputTokens != 10 || entries[0].OutputTokens != 178 {
		t.Errorf("empty ledger must fall back to messages: %+v", entries)
	}
}

func TestMalformedElementsDoNotDropTheThread(t *testing.T) {
	// Non-object message elements and a non-object usageLedger degrade to
	// absent instead of failing the whole thread (upstream lenient navigation).
	file := writeFile(t, t.TempDir(), "threads/t.json", `{
		"id": "T-bad",
		"usageLedger": "not-an-object",
		"messages": [
			"garbage",
			{"role": "assistant", "usage": {"model": "m", "inputTokens": 10,
			 "outputTokens": 178, "timestamp": "2026-01-19T11:42:10.652Z"}}
		]
	}`)
	entries, err := parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].InputTokens != 10 || entries[0].OutputTokens != 178 {
		t.Errorf("malformed elements must be skipped: %+v", entries)
	}
}

func TestLedgerTakesPrecedenceOverMessages(t *testing.T) {
	file := writeFile(t, t.TempDir(), "threads/t.json", `{
		"id": "thread-a",
		"usageLedger": {"events": [{"id": "event-a",
			"timestamp": "2026-01-02T00:00:00.000Z", "model": "gpt-5",
			"tokens": {"input": 1, "output": 2}}]},
		"messages": [{"role": "assistant", "usage": {"model": "claude-haiku",
			"inputTokens": 99, "outputTokens": 99,
			"timestamp": "2026-01-19T11:42:10.652Z"}}]
	}`)
	entries, err := parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Model != "gpt-5" || entries[0].InputTokens != 1 {
		t.Errorf("ledger must win over messages: %+v", entries)
	}
}

func TestLedgerWithoutToMessageIDHasNoCache(t *testing.T) {
	// Ledger tokens.cache is ignored; without to_message_id the cache buckets
	// stay zero (upstream unwrap_or_default).
	file := writeFile(t, t.TempDir(), "threads/t.json", `{
		"id": "T-nocache",
		"usageLedger": {"events": [{"id": "e1",
			"timestamp": "2026-01-02T00:00:00.000Z", "model": "m",
			"tokens": {"input": 10, "output": 5, "cache": {"read": 999, "write": 888},
			"total": 15}}]}
	}`)
	entries, err := parse(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if e := entries[0]; e.CacheReadTokens != 0 || e.CacheCreationTokens != 0 {
		t.Errorf("ledger cache must be ignored: cc=%d cr=%d", e.CacheCreationTokens, e.CacheReadTokens)
	}
}
