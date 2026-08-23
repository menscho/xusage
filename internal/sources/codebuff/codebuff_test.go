package codebuff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// writeChat materializes one chat-messages.json under a temp Codebuff layout
// and returns its absolute path plus the session id derived from that layout.
// As upstream, the channel component of the session id is the data root's own
// directory name.
func writeChat(t *testing.T, root, project, chatID, content string) (string, string) {
	t.Helper()
	dir := filepath.Join(root, "projects", project, "chats", chatID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "chat-messages.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Base(root) + "/" + project + "/" + chatID
	return path, session
}

func TestParseChatFileHappyPath(t *testing.T) {
	root := t.TempDir()
	path, wantSession := writeChat(t, root, "proj-a", "2026-01-02T03-04-05.000Z", `[
		{"role":"user","text":"hello"},
		{
			"id":"msg-1",
			"role":"assistant",
			"timestamp":"2026-01-02T03:04:06.000Z",
			"metadata":{"model":"claude-sonnet-4-20250514","usage":{"inputTokens":100,"outputTokens":50,"cacheCreationInputTokens":20,"cacheReadInputTokens":10}},
			"credits":1.25
		}
	]`)
	entries, err := ParseChatFile(path)
	if err != nil {
		t.Fatalf("ParseChatFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	wantTime := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	if !e.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, wantTime)
	}
	if e.ID != "codebuff:"+wantSession+":msg-1" {
		t.Errorf("ID = %q, want codebuff:%s:msg-1", e.ID, wantSession)
	}
	if e.SessionID != wantSession {
		t.Errorf("SessionID = %q, want %q", e.SessionID, wantSession)
	}
	if e.Model != "claude-sonnet-4-20250514" || e.RawModel != e.Model {
		t.Errorf("Model/RawModel = %q/%q", e.Model, e.RawModel)
	}
	if e.InputTokens != 100 || e.OutputTokens != 50 ||
		e.CacheCreationTokens != 20 || e.CacheReadTokens != 10 {
		t.Errorf("token split wrong: %+v", e)
	}
	if e.Messages != 1 {
		t.Errorf("Messages = %d, want 1", e.Messages)
	}
}

func TestParseChatFileSnakeCaseAndRunStateFallback(t *testing.T) {
	root := t.TempDir()
	path, _ := writeChat(t, root, "proj-b", "2026-01-02T03-04-05.000Z", `[
		{"variant":"agent","metadata":{"runState":{"sessionState":{"mainAgentState":{"messageHistory":[
			{"role":"user","providerOptions":{}},
			{"role":"assistant","providerOptions":{"codebuff":{"model":"openai/gpt-5","usage":{"prompt_tokens":100,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":10}}}}}
		]}}}}}
	]`)
	entries, err := ParseChatFile(path)
	if err != nil {
		t.Fatalf("ParseChatFile: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", e.Model)
	}
	if e.InputTokens != 100 || e.OutputTokens != 50 || e.CacheReadTokens != 10 {
		t.Errorf("token split wrong: %+v", e)
	}
}

func TestParseUsageObjectTotalTokenFallback(t *testing.T) {
	t.Run("total fills missing output tokens", func(t *testing.T) {
		u := parseUsageObject(map[string]json.RawMessage{
			"totalTokens": json.RawMessage(`789`),
		})
		if u.outputTokens != 789 || u.extraTotal != 0 {
			t.Errorf("output=%d extra=%d, want 789/0", u.outputTokens, u.extraTotal)
		}
	})
	t.Run("leftover beyond known buckets becomes extra total", func(t *testing.T) {
		u := parseUsageObject(map[string]json.RawMessage{
			"inputTokens":  json.RawMessage(`100`),
			"outputTokens": json.RawMessage(`50`),
			"totalTokens":  json.RawMessage(`200`),
		})
		if u.outputTokens != 50 || u.extraTotal != 50 {
			t.Errorf("output=%d extra=%d, want 50/50", u.outputTokens, u.extraTotal)
		}
	})
	t.Run("no leftover when total matches known buckets", func(t *testing.T) {
		u := parseUsageObject(map[string]json.RawMessage{
			"inputTokens":          json.RawMessage(`100`),
			"cacheReadInputTokens": json.RawMessage(`25`),
			"totalTokens":          json.RawMessage(`125`),
		})
		if u.inputTokens != 100 || u.cacheRead != 25 || u.extraTotal != 0 {
			t.Errorf("unexpected split: %+v", u)
		}
	})
}

func TestParseChatFileSkips(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{
			name:    "user messages carry no usage",
			content: `[{"role":"user"},{"role":"user"}]`,
			want:    0,
		},
		{
			name:    "assistant without signal skipped",
			content: `[{"role":"assistant","metadata":{"model":"m"}}]`,
			want:    0,
		},
		{
			name:    "non-object array members skipped",
			content: `["garbage",42,null,{"role":"assistant","metadata":{"usage":{"inputTokens":5}}}]`,
			want:    1,
		},
		{
			name:    "empty array",
			content: `[]`,
			want:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, _ := writeChat(t, t.TempDir(), "p", "chat", tt.content)
			entries, err := ParseChatFile(path)
			if err != nil {
				t.Fatalf("ParseChatFile: %v", err)
			}
			if len(entries) != tt.want {
				t.Fatalf("got %d entries, want %d (%+v)", len(entries), tt.want, entries)
			}
		})
	}
}

func TestParseChatFileMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat-messages.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := ParseChatFile(path)
	if err != nil {
		t.Fatalf("malformed file must not error, got %v", err)
	}
	if entries != nil {
		t.Errorf("expected no entries, got %+v", entries)
	}
}

func TestParseChatFileTimestampFallbacks(t *testing.T) {
	t.Run("falls back to chat id timestamp", func(t *testing.T) {
		path, _ := writeChat(t, t.TempDir(), "p", "2026-03-04T05-06-07.000Z",
			`[{"role":"assistant","metadata":{"usage":{"inputTokens":3}}}]`)
		entries, err := ParseChatFile(path)
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries=%d err=%v", len(entries), err)
		}
		want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
		if !entries[0].Timestamp.Equal(want) {
			t.Errorf("Timestamp = %v, want %v", entries[0].Timestamp, want)
		}
	})
	t.Run("falls back to file mtime when nothing else known", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "projects", "p", "chats", "not-a-timestamp")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "chat-messages.json")
		body := `[{"role":"assistant","metadata":{"usage":{"inputTokens":3}}}]`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := ParseChatFile(path)
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries=%d err=%v", len(entries), err)
		}
		got := entries[0].Timestamp.Truncate(time.Second)
		want := fi.ModTime().UTC().Truncate(time.Second)
		if got.Before(want.Add(-time.Second)) || got.After(want.Add(time.Second)) {
			t.Errorf("Timestamp = %v, want within 1s of mtime %v", got, want)
		}
	})
}

func TestDiscoverChatFilesAndMissingDirs(t *testing.T) {
	newRoot := func(t *testing.T) string { return t.TempDir() }

	t.Run("env override wins and finds nested chats", func(t *testing.T) {
		root := newRoot(t)
		p1, _ := writeChat(t, root, "alpha", "chat-1", `[]`)
		p2, _ := writeChat(t, root, "beta", "chat-2", `[]`)
		// An unrelated JSON file must be ignored.
		stray := filepath.Join(root, "projects", "alpha", "other.json")
		if err := os.WriteFile(stray, []byte("[]"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envDataDir, root)

		files, err := DiscoverChatFiles()
		if err != nil {
			t.Fatalf("DiscoverChatFiles: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("got %v, want 2 files", files)
		}
		found := map[string]bool{filepath.Clean(p1): true, filepath.Clean(p2): true}
		for _, f := range files {
			if !found[filepath.Clean(f)] {
				t.Errorf("unexpected file %v", f)
			}
		}
	})

	t.Run("missing data dir returns nil nil", func(t *testing.T) {
		t.Setenv(envDataDir, filepath.Join(t.TempDir(), "does-not-exist"))
		files, err := DiscoverChatFiles()
		if err != nil {
			t.Fatalf("missing dir must not error, got %v", err)
		}
		if files != nil {
			t.Errorf("expected nil files, got %v", files)
		}
	})

	t.Run("root pointing directly at projects dir works", func(t *testing.T) {
		root := newRoot(t)
		p1, _ := writeChat(t, root, "alpha", "chat-1", `[]`)
		t.Setenv(envDataDir, filepath.Join(root, "projects"))
		files, err := DiscoverChatFiles()
		if err != nil {
			t.Fatalf("DiscoverChatFiles: %v", err)
		}
		if len(files) != 1 || filepath.Clean(files[0]) != filepath.Clean(p1) {
			t.Fatalf("got %v, want [%s]", files, p1)
		}
	})

	t.Run("env list splits on commas only like upstream", func(t *testing.T) {
		first := newRoot(t)
		second := newRoot(t)
		p1, _ := writeChat(t, first, "alpha", "chat-1", `[]`)
		p2, _ := writeChat(t, second, "beta", "chat-2", `[]`)
		// A semicolon-joined path is one (nonexistent) entry upstream and must
		// not silently split into two roots.
		t.Setenv(envDataDir, first+";"+second+","+second)
		files, err := DiscoverChatFiles()
		if err != nil {
			t.Fatalf("DiscoverChatFiles: %v", err)
		}
		found := map[string]bool{}
		for _, f := range files {
			found[filepath.Clean(f)] = true
		}
		if !found[filepath.Clean(p2)] {
			t.Fatalf("comma-separated root missing from %v", files)
		}
		if found[filepath.Clean(p1)] {
			t.Errorf("semicolon part must not be treated as a separator: %v", files)
		}
	})

	t.Run("case-variant Projects base name is not treated as a projects root", func(t *testing.T) {
		root := newRoot(t)
		writeChat(t, root, "alpha", "chat-1", `[]`)
		// Upstream compares the base name exactly ("projects"), so a
		// case-variant root gets "/projects" appended instead.
		t.Setenv(envDataDir, filepath.Join(root, "Projects"))
		files, err := DiscoverChatFiles()
		if err != nil {
			t.Fatalf("DiscoverChatFiles: %v", err)
		}
		if len(files) != 0 {
			t.Fatalf("got %v, want none: Projects must not match exactly", files)
		}
	})
}

func TestSourceRegistered(t *testing.T) {
	for _, s := range sources.All() {
		if s.Name == "codebuff" {
			if s.Discover == nil || s.Parse == nil {
				t.Fatal("registered codebuff source has nil Discover or Parse")
			}
			return
		}
	}
	t.Fatal("codebuff source not registered")
}

func TestDedupKeyStableForSameMessage(t *testing.T) {
	msg := map[string]json.RawMessage{"id": json.RawMessage(`"m-1"`)}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := dedupKey(msg, "sess", ts, "claude-x", assistantUsage{inputTokens: 1}, 0)
	b := dedupKey(msg, "sess", ts, "claude-x", assistantUsage{inputTokens: 9}, 7)
	if a != b {
		t.Errorf("id-backed dedup key should ignore usage/ordinal: %q vs %q", a, b)
	}
	fallbackA := dedupKey(map[string]json.RawMessage{}, "sess", ts, "claude-x", assistantUsage{inputTokens: 1}, 0)
	fallbackB := dedupKey(map[string]json.RawMessage{}, "sess", ts, "claude-x", assistantUsage{inputTokens: 1}, 0)
	if fallbackA != fallbackB {
		t.Errorf("fallback key should be deterministic: %q vs %q", fallbackA, fallbackB)
	}
	if fallbackA == a {
		t.Error("fallback key must differ from id-backed key")
	}
}

// compile-time assertion that ParseChatFile satisfies the source Parse shape.
var _ func(string) ([]model.Entry, error) = ParseChatFile
