package qwen

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeChat materializes a chat file under the projects/<p>/chats layout.
func writeChat(t *testing.T, root, project, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, "projects", project, "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const fullLine = `{"type":"assistant","timestamp":"2025-06-01T12:00:00Z","sessionId":"sess-1",` +
	`"model":"qwen3-coder-plus","usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":200,` +
	`"thoughtsTokenCount":50,"cachedContentTokenCount":300,"totalTokenCount":1550}}`

func TestParseHappyPath(t *testing.T) {
	path := writeChat(t, t.TempDir(), "proj", "chat.jsonl",
		fullLine+"\n"+`{"type":"user","message":"hi"}`+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "qwen3-coder-plus" || e.SessionID != "sess-1" || e.Project != "proj" {
		t.Errorf("bad identity: %+v", e)
	}
	if e.InputTokens != 1000 || e.OutputTokens != 200 || e.ExtraTotalTokens != 50 {
		// Upstream apply_total_token_fallback: thoughts start in extra (they
		// are unpriced), so out stays at candidatesTokenCount and the total
		// footprint is unchanged (1000+200+300+50 = 1550 = totalTokenCount).
		t.Errorf("bad in/out/extra: %d/%d/%d", e.InputTokens, e.OutputTokens, e.ExtraTotalTokens)
	}
	if e.CacheReadTokens != 300 {
		t.Errorf("cache read = %d, want 300", e.CacheReadTokens)
	}
	if e.ExtraTotalTokens == 0 {
		t.Logf("total fallback spilled nothing; total=%d known=%d", 1550,
			e.InputTokens+e.OutputTokens+e.CacheReadTokens)
	}
}

func TestParseMalformedLinesSkipped(t *testing.T) {
	content := "{broken\n" + fullLine + "\n[]\n"
	path := writeChat(t, t.TempDir(), "proj", "chat.jsonl", content)
	if got := len(parseFile(path)); got != 1 {
		t.Fatalf("want 1 entry after skipping malformed lines, got %d", got)
	}
}

func TestParseNonAssistantAndEmptyUsageSkipped(t *testing.T) {
	content := `{"type":"user","usageMetadata":{"promptTokenCount":5}}` + "\n" +
		`{"type":"assistant","usageMetadata":{"promptTokenCount":0}}` + "\n"
	path := writeChat(t, t.TempDir(), "proj", "chat.jsonl", content)
	if got := parseFile(path); len(got) != 0 {
		t.Fatalf("want no entries, got %d", len(got))
	}
}

func TestTotalOnlyBackfillsOutput(t *testing.T) {
	content := `{"type":"assistant","timestamp":"2025-06-01T12:00:00Z",` +
		`"sessionId":"s","model":"m","usageMetadata":{"totalTokenCount":777}}`
	path := writeChat(t, t.TempDir(), "proj", "chat.jsonl", content)
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].OutputTokens != 777 {
		t.Fatalf("want output backfilled to 777, got %+v", entries)
	}
}

// Zero-output backfill: a total above the known buckets fills output first
// when candidatesTokenCount is absent (upstream apply_total_token_fallback),
// instead of parking the entire gap in ExtraTotalTokens.
func TestTotalGapBackfillsZeroOutput(t *testing.T) {
	content := `{"type":"assistant","timestamp":"2025-06-01T12:00:00Z",` +
		`"sessionId":"s","model":"m","usageMetadata":{"promptTokenCount":100,` +
		`"totalTokenCount":175}}`
	path := writeChat(t, t.TempDir(), "proj", "chat.jsonl", content)
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].OutputTokens != 75 || entries[0].ExtraTotalTokens != 0 {
		t.Fatalf("want out=75 extra=0, got %+v", entries)
	}
}

// A non-RFC3339 timestamp string is not a valid stamp upstream: the entry
// must carry the file mtime fallback, not a bogus epoch-seconds read.
func TestNonRFC3339TimestampFallsBackToMtime(t *testing.T) {
	content := `{"type":"assistant","timestamp":"not-a-date",` +
		`"sessionId":"s","model":"m","usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}`
	path := writeChat(t, t.TempDir(), "proj", "chat.jsonl", content)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	ts := entries[0].Timestamp
	if ts.Before(time.Unix(1_700_000_000, 0)) || ts.After(fi.ModTime().Add(time.Minute)) {
		t.Errorf("timestamp = %v, want the file mtime (~%v)", ts, fi.ModTime())
	}
}

func TestSessionFallsBackToProjectStem(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2025-06-01T12:00:00Z","model":"m",` +
		`"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}`
	path := writeChat(t, t.TempDir(), "myproj", "chat-x.jsonl", line)
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].SessionID != "myproj-chat-x" {
		t.Fatalf("session fallback failed: %+v", entries)
	}
}

func TestDiscoverLayoutAndMissingDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("QWEN_DATA_DIR", root)
	writeChat(t, root, "alpha", "a.jsonl", fullLine+"\n")
	writeChat(t, root, "beta", "b.jsonl", fullLine+"\n")
	// Wrong layouts that must be ignored:
	writeChatLoose(t, root, "gamma.jsonl") // directly under projects/
	os.MkdirAll(filepath.Join(root, "projects", "delta"), 0o755)

	files := discoverChatFiles()
	if len(files) != 2 {
		t.Fatalf("want 2 discovered files, got %v", files)
	}

	os.RemoveAll(root)
	if files := discoverChatFiles(); len(files) != 0 {
		t.Fatalf("missing dir must yield empty discovery, got %v", files)
	}
}

func writeChatLoose(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "projects", name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectFromFile(t *testing.T) {
	got := projectFromFile(filepath.Join("/r", "projects", "p1", "chats", "c.jsonl"))
	if got != "p1" {
		t.Fatalf("project = %q, want p1", got)
	}
	if projectFromFile(filepath.Join("/r", "other", "c.jsonl")) != "" {
		t.Fatal("non-chats layout must yield empty project")
	}
}
