package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// createdMS is 2026-01-02T00:00:00Z, matching upstream fixture timestamps.
const createdMS = int64(1_767_312_000_000)

// writeFile materializes content at a relative path inside dir.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// msg builds one OpenCode message document. Token values of -1 omit the key;
// use the raw template for anything more exotic.
func msg(id, session, provider, modelID string, in, out, read, write, total, created int64, cost string) string {
	b := map[string]interface{}{"id": id, "sessionID": session}
	if provider != "" {
		b["providerID"] = provider
	}
	if modelID != "" {
		b["modelID"] = modelID
	}
	tok := map[string]interface{}{}
	if in >= 0 {
		tok["input"] = in
	}
	if out >= 0 {
		tok["output"] = out
	}
	if total >= 0 {
		tok["total"] = total
	}
	if read >= 0 || write >= 0 {
		cache := map[string]interface{}{}
		if read >= 0 {
			cache["read"] = read
		}
		if write >= 0 {
			cache["write"] = write
		}
		tok["cache"] = cache
	}
	b["tokens"] = tok
	if created >= 0 {
		b["time"] = map[string]interface{}{"created": created}
	}
	if cost != "" {
		b["cost"] = json.RawMessage(cost)
	}
	data, err := json.Marshal(b)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// mustParse runs the registered Parse and fails the test on error.
func mustParse(t *testing.T, path string) []model.Entry {
	t.Helper()
	ents, err := parseFile(path)
	if err != nil {
		t.Fatalf("parseFile(%s): %v", path, err)
	}
	return ents
}

func TestHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "storage/message/ses_a/msg-1.json",
		msg("msg-1", "ses_a", "anthropic", "claude-sonnet-4-20250514",
			100, 50, 10, 20, 180, createdMS, "0.02"))

	ents := mustParse(t, filepath.Join(dir, "storage/message/ses_a/msg-1.json"))
	if len(ents) != 1 {
		t.Fatalf("got %d entries, want 1", len(ents))
	}
	e := ents[0]
	wantTime := time.UnixMilli(createdMS).UTC()
	if e.ID != "msg-1" {
		t.Errorf("ID = %q, want %q", e.ID, "msg-1")
	}
	if e.SessionID != "ses_a" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "ses_a")
	}
	if e.Model != "claude-sonnet-4-20250514" || e.RawModel != "claude-sonnet-4-20250514" {
		t.Errorf("Model/RawModel = %q/%q, want claude-sonnet-4-20250514", e.Model, e.RawModel)
	}
	if e.Project != projectLabel {
		t.Errorf("Project = %q, want %q", e.Project, projectLabel)
	}
	if e.InputTokens != 100 || e.OutputTokens != 50 {
		t.Errorf("input/output = %d/%d, want 100/50", e.InputTokens, e.OutputTokens)
	}
	if e.CacheCreationTokens != 20 {
		t.Errorf("CacheCreationTokens = %d, want 20 (cache.write)", e.CacheCreationTokens)
	}
	if e.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens = %d, want 10 (cache.read)", e.CacheReadTokens)
	}
	if e.ExtraTotalTokens != 0 {
		t.Errorf("ExtraTotalTokens = %d, want 0 (total <= known buckets)", e.ExtraTotalTokens)
	}
	if !e.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, wantTime)
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.02 {
		t.Errorf("ReportedCost = %v, want 0.02", e.ReportedCost)
	}
}

func TestTotalTokenFallback(t *testing.T) {
	tests := []struct {
		name          string
		in, out       int64 // -1 omits the key
		read, write   int64
		total         int64
		wantOut       uint64
		wantExtra     uint64
		wantIn        uint64
		wantCacheRead uint64
	}{
		{
			name: "missing output topped up from total",
			in:   100, out: 0, read: 25, write: -1, total: 175,
			wantIn: 100, wantOut: 50, wantCacheRead: 25,
		},
		{
			name: "remainder beyond known buckets becomes extra",
			in:   100, out: 50, read: 25, write: -1, total: 200,
			wantIn: 100, wantOut: 50, wantCacheRead: 25, wantExtra: 25,
		},
		{
			name: "total only fills output",
			in:   -1, out: -1, read: -1, write: -1, total: 123,
			wantOut: 123,
		},
		{
			name: "total below known buckets changes nothing",
			in:   100, out: 50, read: -1, write: -1, total: 100,
			wantIn: 100, wantOut: 50,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := msg("m", "s", "openai", "gpt-test", tt.in, tt.out, tt.read, tt.write, tt.total, createdMS, "")
			e := parseMessage([]byte(line))
			if e == nil {
				t.Fatal("entry unexpectedly skipped")
			}
			if e.InputTokens != tt.wantIn || e.OutputTokens != tt.wantOut ||
				e.CacheReadTokens != tt.wantCacheRead || e.ExtraTotalTokens != tt.wantExtra {
				t.Errorf("got in=%d out=%d read=%d extra=%d, want in=%d out=%d read=%d extra=%d",
					e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.ExtraTotalTokens,
					tt.wantIn, tt.wantOut, tt.wantCacheRead, tt.wantExtra)
			}
		})
	}
}

func TestSkippedRecords(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		wantKept  int    // well-formed records expected to survive
		wantFirst string // ID of the first surviving entry ("" = don't check)
	}{
		{
			name:     "missing token block",
			lines:    []string{`{"id":"m","sessionID":"s","providerID":"p","modelID":"gpt-test"}`},
			wantKept: 0,
		},
		{
			name:     "null token block",
			lines:    []string{`{"id":"m","sessionID":"s","providerID":"p","modelID":"gpt-test","tokens":null}`},
			wantKept: 0,
		},
		{
			name:     "empty model",
			lines:    []string{msg("m", "s", "p", "", 10, 5, -1, -1, -1, createdMS, "")},
			wantKept: 0,
		},
		{
			name:     "missing provider",
			lines:    []string{msg("m", "s", "", "gpt-test", 10, 5, -1, -1, -1, createdMS, "")},
			wantKept: 0,
		},
		{
			name:     "all-zero tokens",
			lines:    []string{msg("m", "s", "p", "gpt-test", 0, 0, 0, 0, 0, createdMS, "")},
			wantKept: 0,
		},
		{
			name: "malformed line skipped, valid neighbors kept",
			lines: []string{
				msg("m1", "s", "p", "gpt-test", 10, 5, -1, -1, -1, createdMS, ""),
				`{"broken json,,,`,
				msg("m2", "s", "p", "gpt-test", 20, 5, -1, -1, -1, createdMS, ""),
			},
			wantKept:  2,
			wantFirst: "m1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "messages.jsonl")
			body := ""
			for _, l := range tt.lines {
				body += l + "\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			ents := mustParse(t, path)
			if len(ents) != tt.wantKept {
				t.Fatalf("got %d entries, want %d", len(ents), tt.wantKept)
			}
			if tt.wantFirst != "" && ents[0].ID != tt.wantFirst {
				t.Errorf("first ID = %q, want %q", ents[0].ID, tt.wantFirst)
			}
		})
	}
}

func TestLenientNumbers(t *testing.T) {
	line := `{"id":"m","sessionID":"s","providerID":"p","modelID":"gpt-test",` +
		`"tokens":{"input":"100","output":12.5,"cache":{"read":-5,"write":null},"total":true},` +
		`"time":{"created":` + fmt.Sprint(createdMS) + `},"cost":null}`
	e := parseMessage([]byte(line))
	if e == nil {
		t.Fatal("entry unexpectedly skipped")
	}
	if e.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (numeric string accepted)", e.InputTokens)
	}
	if e.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0 (fractional rejected)", e.OutputTokens)
	}
	if e.CacheReadTokens != 0 || e.CacheCreationTokens != 0 {
		t.Errorf("cache read/write = %d/%d, want 0/0 (negative and null rejected)",
			e.CacheReadTokens, e.CacheCreationTokens)
	}
	wantTime := time.UnixMilli(createdMS).UTC()
	if !e.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp = %v, want %v", e.Timestamp, wantTime)
	}
	if e.ReportedCost != nil {
		t.Errorf("ReportedCost = %v, want nil (null cost)", e.ReportedCost)
	}
}

func TestLenientStringTimestampAndCost(t *testing.T) {
	// Upstream parses these fields leniently: numeric strings are accepted.
	line := `{"id":"m","sessionID":"s","providerID":"p","modelID":"gpt-test",` +
		`"tokens":{"input":10,"output":5},` +
		`"time":{"created":"` + fmt.Sprint(createdMS) + `"},"cost":"0.25"}`
	e := parseMessage([]byte(line))
	if e == nil {
		t.Fatal("entry unexpectedly skipped")
	}
	if !e.Timestamp.Equal(time.UnixMilli(createdMS).UTC()) {
		t.Errorf("Timestamp = %v, want %v (numeric string accepted)", e.Timestamp, time.UnixMilli(createdMS).UTC())
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0.25 {
		t.Errorf("ReportedCost = %v, want 0.25 (numeric string accepted)", e.ReportedCost)
	}
}

func TestZeroCostIsReported(t *testing.T) {
	line := msg("m", "s", "p", "gpt-test", 10, 5, -1, -1, -1, createdMS, "0")
	e := parseMessage([]byte(line))
	if e == nil {
		t.Fatal("entry unexpectedly skipped")
	}
	if e.ReportedCost == nil || *e.ReportedCost != 0 {
		t.Errorf("ReportedCost = %v, want &0 (explicit zero preserved)", e.ReportedCost)
	}
}

func TestMissingCostAndTime(t *testing.T) {
	line := `{"id":"m","sessionID":"s","providerID":"p","modelID":"gpt-test","tokens":{"input":10}}`
	e := parseMessage([]byte(line))
	if e == nil {
		t.Fatal("entry unexpectedly skipped")
	}
	if e.ReportedCost != nil {
		t.Errorf("ReportedCost = %v, want nil", e.ReportedCost)
	}
	if !e.Timestamp.IsZero() {
		t.Errorf("Timestamp = %v, want zero", e.Timestamp)
	}
}

func TestDedupRelevantFieldsPresent(t *testing.T) {
	// Two files carrying the same message ID must expose identical identity
	// fields so the engine's loader-side dedup can collapse them.
	a := msg("dup-1", "ses_x", "p", "gpt-test", 10, 5, -1, -1, -1, createdMS, "")
	b := msg("dup-1", "ses_y", "p", "gpt-test", 99, 5, -1, -1, -1, createdMS, "")
	dir := t.TempDir()
	writeFile(t, dir, "storage/message/ses_x/dup-1.json", a)
	writeFile(t, dir, "storage/message/ses_y/dup-1.json", b)

	var got []model.Entry
	for _, rel := range []string{"storage/message/ses_x/dup-1.json", "storage/message/ses_y/dup-1.json"} {
		got = append(got, mustParse(t, filepath.Join(dir, filepath.FromSlash(rel)))...)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 before dedup", len(got))
	}
	if got[0].ID != got[1].ID || got[0].ID == "" {
		t.Errorf("duplicate detection requires equal non-empty IDs, got %q vs %q",
			got[0].ID, got[1].ID)
	}
	if got[0].Provider != "" && got[0].Provider != "opencode" {
		// Adapters leave Provider empty; the engine stamps it.
		t.Errorf("adapter must not set Provider, got %q", got[0].Provider)
	}
}

func TestDiscoverLayout(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "storage/message/ses_a/msg-1.json", "{}")
	writeFile(t, root, "storage/message/ses_a/nested/msg-2.json", "{}")
	writeFile(t, root, "storage/message/notes.txt", "not json")
	writeFile(t, root, "opencode.db", "not readable as messages")

	t.Setenv(envDataDir, root)
	files, err := discover()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := []string{
		filepath.Join(root, "storage", "message", "ses_a", "msg-1.json"),
		filepath.Join(root, "storage", "message", "ses_a", "nested", "msg-2.json"),
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}
}

func TestDiscoverMultipleRoots(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFile(t, a, "storage/message/x/a1.json", "{}")
	writeFile(t, b, "storage/message/y/b1.json", "{}")

	t.Setenv(envDataDir, a+","+b)
	files, err := discover()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files (%v), want 2", len(files), files)
	}
}

func TestDiscoverMissingDirsReturnNilNil(t *testing.T) {
	t.Setenv(envDataDir, filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := discover()
	if err != nil {
		t.Fatalf("discover on missing dir: %v", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}

	// Root exists but has no storage/message subtree.
	empty := t.TempDir()
	t.Setenv(envDataDir, empty)
	files, err = discover()
	if err != nil {
		t.Fatalf("discover on root without storage: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want none", files)
	}
}

func TestEnvOverridesHomeDefault(t *testing.T) {
	// With the variable pointing at a populated temp root, discovery must see
	// exactly that root's files even though the machine may also carry a real
	// ~/.local/share/opencode install.
	root := t.TempDir()
	writeFile(t, root, "storage/message/s/msg.json", "{}")
	t.Setenv(envDataDir, root+","+filepath.Join(t.TempDir(), "missing"))

	files, err := discover()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 || files[0] != filepath.Join(root, "storage", "message", "s", "msg.json") {
		t.Errorf("files = %v, want the single env-root file", files)
	}
}

func TestParseFileErrorOnDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := parseFile(dir); err == nil {
		t.Error("parsing a directory should return an error")
	}
}

func TestRegisteredInGlobalRegistry(t *testing.T) {
	for _, s := range sources.All() {
		if s.Name == "opencode" {
			if s.Discover == nil || s.Parse == nil {
				t.Fatal("registered opencode source has nil Discover/Parse")
			}
			return
		}
	}
	t.Error("opencode source not found in sources.All()")
}

func TestLenientNonObjectNestedBlocks(t *testing.T) {
	// Upstream treats a non-object `cache`/`time` as absent instead of
	// dropping the record (jsonl::lenient_object).
	line := `{"id":"m","sessionID":"s","providerID":"p","modelID":"gpt-test",` +
		`"tokens":{"input":100,"output":10,"cache":0},"time":5}`
	if e := parseMessage([]byte(line)); e == nil {
		t.Fatal("non-object cache/time must not drop the record")
	} else {
		if e.InputTokens != 100 || e.OutputTokens != 10 {
			t.Errorf("tokens wrong: %+v", e)
		}
		if e.CacheReadTokens != 0 || e.CacheCreationTokens != 0 {
			t.Errorf("non-object cache must read as absent, got read=%d write=%d",
				e.CacheReadTokens, e.CacheCreationTokens)
		}
		if !e.Timestamp.IsZero() {
			t.Errorf("non-object time must read as absent, got %v", e.Timestamp)
		}
	}
}

func TestEmptySessionIDFallsBackToUnknown(t *testing.T) {
	// Upstream loaded_session_id: absent or empty sessionID becomes "unknown".
	line := `{"id":"m","sessionID":"","providerID":"p","modelID":"gpt-test","tokens":{"input":1}}`
	e := parseMessage([]byte(line))
	if e == nil {
		t.Fatal("entry unexpectedly skipped")
	}
	if e.SessionID != "unknown" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "unknown")
	}
}

func TestDiscoverDeduplicatesRepeatedRoots(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "storage/message/s/a.json", "{}")
	t.Setenv(envDataDir, root+","+root)

	files, err := discover()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("files = %v, want the single file once", files)
	}
}
