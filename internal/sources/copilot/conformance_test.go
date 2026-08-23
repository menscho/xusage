package copilot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Upstream is_span_record: an explicit "type":"span" wins even when the
// record carries no span coordinates. The old heuristic demanded a name plus
// coordinates and misclassified such spans as logs.
func TestExplicitSpanTypeClassifiesAsSpan(t *testing.T) {
	line := `{"type":"span","name":"chat gpt-4.1",` +
		`"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"m",` +
		`"gen_ai.usage.input_tokens":10,"gen_ai.usage.output_tokens":5}}`
	path := writeOTel(t, t.TempDir(), "spans.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", entries)
	}
	// Without ids/timestamps the key falls back to span:session:millis:index;
	// the class prefix is what pins the record as a span.
	if !strings.HasPrefix(entries[0].ID, "span:unknown-session:") {
		t.Fatalf("explicit span must keep the span dedup key, got %q", entries[0].ID)
	}
}

// A non-string type field (or padded value) must not flip the record into the
// span class; upstream compares the raw string exactly.
func TestNonStringTypeFallsBackToCoordinates(t *testing.T) {
	// type 5 + no coordinates -> not a span; with event.name it is a log.
	line := `{"type":5,"attributes":{"event.name":"gen_ai.client.inference.operation.details",` +
		`"gen_ai.usage.total_tokens":50,"gen_ai.response.id":"resp-9"}}`
	path := writeOTel(t, t.TempDir(), "mixed.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].OutputTokens != 50 {
		t.Errorf("output = %d, want total backfilled to 50", entries[0].OutputTokens)
	}
}

// Padded discriminators do not match exactly in upstream (its own gemini
// test pins this), and the early return means a string type — any string —
// skips the coordinate fallback entirely: the record classifies as nothing
// and emits no entry.
func TestPaddedSpanTypeDoesNotMatchExactly(t *testing.T) {
	line := `{"type":" span ","name":"chat gpt-4.1","traceId":"tr-pad","spanId":"sp-pad",` +
		`"attributes":{"gen_ai.operation.name":"chat","gen_ai.usage.input_tokens":8,"gen_ai.usage.output_tokens":2}}`
	path := writeOTel(t, t.TempDir(), "padded.jsonl", line+"\n")
	if entries := parseFile(path); len(entries) != 0 {
		t.Fatalf("padded type must match neither span nor log, got %+v", entries)
	}
	// The same record without the type field classifies via coordinates.
	noType := `{"name":"chat gpt-4.1","traceId":"tr-pad","spanId":"sp-pad",` +
		`"attributes":{"gen_ai.operation.name":"chat","gen_ai.usage.input_tokens":8,"gen_ai.usage.output_tokens":2}}`
	path = writeOTel(t, t.TempDir(), "notype.jsonl", noType+"\n")
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].ID != "tr-pad:sp-pad" {
		t.Fatalf("coordinate fallback must classify the span, got %+v", entries)
	}
}

// Model/session ids may live on any earlier record of the same trace
// (collect_trace_contexts): even a usage-less chat span contributes them,
// while producing no candidate of its own — so a later summary span on the
// same trace survives suppression and inherits the identity.
func TestTraceContextSuppliesModelAndSession(t *testing.T) {
	first := `{"type":"span","name":"chat gpt-4.1","traceId":"ctx-trace","spanId":"s1",` +
		`"endTime":[1754038200,0],` +
		`"attributes":{"gen_ai.request.model":"gpt-4.1","copilot_chat.session_id":"conv-77"}}`
	last := `{"type":"span","name":"invoke_agent agent","traceId":"ctx-trace","spanId":"s2",` +
		`"endTime":[1754038300,0],` +
		`"attributes":{"gen_ai.operation.name":"invoke_agent",` +
		`"gen_ai.usage.input_tokens":20,"gen_ai.usage.output_tokens":4}}`
	path := writeOTel(t, t.TempDir(), "ctx.jsonl", first+"\n"+last+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry (usage-less chat span emits nothing), got %d: %+v",
			len(entries), entries)
	}
	e := entries[0]
	if e.ID != "ctx-trace:s2" {
		t.Fatalf("ID = %q, want ctx-trace:s2", e.ID)
	}
	if e.Model != "gpt-4.1" || e.SessionID != "conv-77" {
		t.Errorf("summary span must inherit model gpt-4.1 / session conv-77, got %q / %q",
			e.Model, e.SessionID)
	}
}

// A chat span carrying real usage suppresses a same-trace summary span
// (upstream AgentSummarySpan arm checks every lower class).
func TestChatSpanSuppressesSameTraceSummarySpan(t *testing.T) {
	first := `{"type":"span","name":"chat gpt-4.1","traceId":"dup-trace","spanId":"s1",` +
		`"attributes":{"gen_ai.operation.name":"chat",` +
		`"gen_ai.usage.input_tokens":10,"gen_ai.usage.output_tokens":2}}`
	last := `{"type":"span","name":"invoke_agent agent","traceId":"dup-trace","spanId":"s2",` +
		`"attributes":{"gen_ai.operation.name":"invoke_agent",` +
		`"gen_ai.usage.input_tokens":20,"gen_ai.usage.output_tokens":4}}`
	path := writeOTel(t, t.TempDir(), "dup.jsonl", first+"\n"+last+"\n")
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].ID != "dup-trace:s1" {
		t.Fatalf("chat span must win over same-trace summary, got %+v", entries)
	}
}

// Upstream timestamp_from_scalar accepts string scalars and treats sub-100b
// values as epoch seconds.
func TestScalarTimestampAsStringSeconds(t *testing.T) {
	line := `{"type":"span","name":"chat m","timestamp":"1750000000",` +
		`"attributes":{"gen_ai.usage.output_tokens":3,"gen_ai.response.model":"m"}}`
	path := writeOTel(t, t.TempDir(), "scalar.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if want := int64(1750000000000); entries[0].Timestamp.UnixMilli() != want {
		t.Errorf("timestamp ms = %d, want %d", entries[0].Timestamp.UnixMilli(), want)
	}
}

// A turn.index attribute feeds the agent-turn dedup key instead of the line
// index (upstream dedup_key_for_record AgentTurnLog arm).
func TestAgentTurnDedupKeyUsesTurnIndexAttr(t *testing.T) {
	line := `{"body":"copilot_chat.agent.turn started",` +
		`"attributes":{"event.name":"copilot_chat.agent.turn","turn.index":7,` +
		`"gen_ai.usage.output_tokens":6,"gen_ai.conversation.id":"conv-t"}}`
	path := writeOTel(t, t.TempDir(), "turn.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].ID != "agent-turn:conv-t:7:0" {
		t.Errorf("ID = %q, want agent-turn keyed by conversation+turn index", entries[0].ID)
	}
}

// An explicit zero total stays present for the fallback (known==0 path), and a
// float-formatted counter parses like upstream number_value.
func TestFloatFormattedCountersParse(t *testing.T) {
	line := `{"type":"span","name":"chat m",` +
		`"attributes":{"gen_ai.usage.input_tokens":120.0,"gen_ai.usage.cache_read_tokens":20.5,` +
		`"gen_ai.response.model":"m"}}`
	path := writeOTel(t, t.TempDir(), "floats.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.InputTokens != 100 || e.CacheReadTokens != 20 { // trunc(20.5) split off input
		t.Errorf("split = (%d, read %d), want (100, 20)", e.InputTokens, e.CacheReadTokens)
	}
}

// spanContext nested ids feed the dedup key when flat fields are absent.
func TestSpanContextNestedIDs(t *testing.T) {
	line := `{"type":"span","name":"chat m","spanContext":{"traceId":"sc-trace","spanId":"sc-span"},` +
		`"attributes":{"gen_ai.usage.output_tokens":4,"gen_ai.response.model":"m"}}`
	path := writeOTel(t, t.TempDir(), "spanctx.jsonl", line+"\n")
	entries := parseFile(path)
	if len(entries) != 1 || entries[0].ID != "sc-trace:sc-span" {
		t.Fatalf("want spanContext-derived dedup key, got %+v", entries)
	}
}

// Missing files yield zero entries, never a panic (baseline behavior kept).
func TestParseMissingFileYieldsNil(t *testing.T) {
	if entries := parseFile(filepath.Join(t.TempDir(), "nope.jsonl")); entries != nil {
		t.Fatalf("want nil, got %+v", entries)
	}
}

// The env exporter file participates in discovery alongside the default dir,
// and an identical path listed twice collapses (upstream seen-set).
func TestDiscoverDeduplicatesEnvPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".copilot", "otel")
	writeOTel(t, dir, "a.jsonl", "{}\n")
	extra := writeOTel(t, dir, "b.jsonl", "{}\n")
	t.Setenv("COPILOT_OTEL_FILE_EXPORTER_PATH", extra+string(filepath.Separator))
	files := discoverFiles()
	found := 0
	for _, f := range files {
		if filepath.Base(f) == "b.jsonl" {
			found++
		}
	}
	if found != 1 {
		t.Errorf("env file with trailing separator must collapse to one entry, got %d in %v",
			found, files)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixture vanished: %v", err)
	}
}
