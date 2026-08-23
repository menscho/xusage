package copilot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeOTel(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spanLine mirrors an upstream-classifiable chat span: type "span", a "chat "
// name, trace/span ids, and an [seconds, nanos] endTime.
const spanLine = `{"type":"span","name":"chat gpt-4.1","traceId":"trace-1","spanId":"span-1",` +
	`"endTime":[1754038200,0],` +
	`"attributes":{` +
	`"gen_ai.operation.name":"chat",` +
	`"gen_ai.request.model":"gpt-4.1",` +
	`"gen_ai.response.model":"gpt-4.1-2025-04-14",` +
	`"gen_ai.conversation.id":"conv-1",` +
	`"gen_ai.usage.input_tokens":2500,` +
	`"gen_ai.usage.output_tokens":120,` +
	`"gen_ai.usage.cache_read.input_tokens":2000,` +
	`"gen_ai.usage.cache_creation.input_tokens":80,` +
	`"gen_ai.usage.reasoning.output_tokens":20}}`

func TestParseHappyPath(t *testing.T) {
	path := writeOTel(t, t.TempDir(), "spans.jsonl", spanLine+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Model != "gpt-4.1-2025-04-14" { // response model wins over request model
		t.Errorf("model = %q", e.Model)
	}
	if e.SessionID != "conv-1" || e.InputTokens != 500 ||
		e.CacheReadTokens != 2000 || e.CacheCreationTokens != 80 {
		t.Errorf("bad mapping: %+v", e)
	}
	if e.OutputTokens != 140 { // 120 + 20 reasoning
		t.Errorf("output = %d, want 140", e.OutputTokens)
	}
	if want := time.UnixMilli(1754038200000).UTC(); !e.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v (endTime [s,n] array)", e.Timestamp, want)
	}
}

// Upstream subtracts cached tokens from OpenAI-style input before the zero
// check: input 2500 with cache_read 2000 yields uncached input 500.
func TestParseSubtractsCacheReadFromInput(t *testing.T) {
	path := writeOTel(t, t.TempDir(), "spans.jsonl", spanLine+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if got := entries[0].InputTokens + entries[0].CacheReadTokens; got != 2500 {
		t.Errorf("input %d + cache read %d != reported input 2500",
			entries[0].InputTokens, entries[0].CacheReadTokens)
	}
}

func TestParseSkipsNonUsageAndMalformed(t *testing.T) {
	content := "{broken\n" +
		`{"attributes":{"http.method":"GET"}}` + "\n" +
		`{"type":"metric","name":"gen_ai.client.token.usage"}` + "\n" +
		spanLine + "\n"
	path := writeOTel(t, t.TempDir(), "spans.jsonl", content)
	if got := parseFile(path); len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
}

// An inference log covering the same response id as a chat span is suppressed
// so one response is never counted twice (upstream should_emit_candidate).
func TestParseSuppressesInferenceLogCoveredByChatSpan(t *testing.T) {
	inference := `{"hrTime":[1754038199,0],` +
		`"attributes":{` +
		`"event.name":"gen_ai.client.inference.operation.details",` +
		`"gen_ai.response.model":"gpt-4.1-2025-04-14",` +
		`"gen_ai.response.id":"resp-1",` +
		`"gen_ai.usage.input_tokens":900,` +
		`"gen_ai.usage.output_tokens":60},` +
		`"_body":"GenAI inference: gpt-4.1-2025-04-14"}`
	chatSpanWithResponse := `{"type":"span","name":"chat gpt-4.1","traceId":"trace-1","spanId":"span-1",` +
		`"endTime":[1754038200,0],` +
		`"attributes":{` +
		`"gen_ai.operation.name":"chat",` +
		`"gen_ai.response.model":"gpt-4.1-2025-04-14",` +
		`"gen_ai.response.id":"resp-1",` +
		`"gen_ai.usage.input_tokens":500,` +
		`"gen_ai.usage.output_tokens":120}}`
	path := writeOTel(t, t.TempDir(), "spans.jsonl", inference+"\n"+chatSpanWithResponse+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry after suppression, got %d", len(entries))
	}
	if entries[0].InputTokens != 500 || entries[0].OutputTokens != 120 {
		t.Errorf("chat span must win: %+v", entries[0])
	}
	// Without the chat span the inference log stands alone.
	path = writeOTel(t, t.TempDir(), "solo.jsonl", inference+"\n")
	if entries = parseFile(path); len(entries) != 1 {
		t.Fatalf("want 1 standalone inference entry, got %d", len(entries))
	}
}

func TestTotalOnlyGoesToOutput(t *testing.T) {
	line := `{"type":"span","name":"chat m","traceId":"trace-1","spanId":"span-1",` +
		`"endTime":[1754038200,0],` +
		`"attributes":{"gen_ai.usage.total_tokens":321,"gen_ai.response.model":"m"}}`
	path := writeOTel(t, t.TempDir(), "spans.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].OutputTokens != 321 {
		t.Fatalf("want total backfilled to output, got %+v", entries)
	}
}

func TestDiscoverDefaultAndEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	writeOTel(t, filepath.Join(home, ".copilot", "otel"), "a.jsonl", spanLine+"\n")
	if err := os.WriteFile(filepath.Join(home, ".copilot", "otel", "b.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	files := discoverFiles()
	if len(files) != 1 || filepath.Base(files[0]) != "a.jsonl" {
		t.Fatalf("discovery = %v", files)
	}

	extra := writeOTel(t, t.TempDir(), "export.jsonl", spanLine+"\n")
	t.Setenv("COPILOT_OTEL_FILE_EXPORTER_PATH", extra)
	files = discoverFiles()
	if len(files) != 2 {
		t.Fatalf("env file not picked up: %v", files)
	}

	os.RemoveAll(home)
	files = discoverFiles() // env file still present
	if len(files) != 1 {
		t.Fatalf("missing default dir must drop out: %v", files)
	}
}
