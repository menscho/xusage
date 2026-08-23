package copilot

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"

	"xusage/internal/model"
	"xusage/internal/sources/jsonx"
)

// flexStr decodes a JSON field that may hold any type: strings are kept,
// every other JSON value (numbers, booleans, null) becomes "" instead of
// failing the whole record, mirroring upstream's Option<Value> fields that
// are read through Value::as_str.
type flexStr string

func (s *flexStr) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		*s = ""
		return nil
	}
	t, ok := v.(string)
	if !ok {
		*s = ""
		return nil
	}
	*s = flexStr(t)
	return nil
}

// raw returns the untrimmed string plus presence (non-empty), mirroring
// Value::as_str — the exact-match discriminators ("span") compare this form.
func (s flexStr) raw() (string, bool) {
	return string(s), s != ""
}

// clean trims surrounding whitespace; ok is false when nothing remains,
// mirroring upstream string_value.
func (s flexStr) clean() (string, bool) {
	t := strings.TrimSpace(string(s))
	return t, t != ""
}

// record mirrors the subset of one OTel JSONL record xusage consumes.
// Timestamp-bearing fields stay raw: exporters emit them as [seconds, nanos]
// arrays, epoch scalars, or nanosecond integers depending on the exporter.
type record struct {
	Type              flexStr         `json:"type"`
	Name              flexStr         `json:"name"`
	SpanID            flexStr         `json:"spanId"`
	TraceID           flexStr         `json:"traceId"`
	SpanContext       map[string]any  `json:"spanContext"`
	StartTime         json.RawMessage `json:"startTime"`
	EndTime           json.RawMessage `json:"endTime"`
	Duration          json.RawMessage `json:"duration"`
	Kind              json.RawMessage `json:"kind"`
	HrTime            json.RawMessage `json:"hrTime"`
	AltHrTime         json.RawMessage `json:"_hrTime"`
	Time              json.RawMessage `json:"time"`
	Timestamp         json.RawMessage `json:"timestamp"`
	ObservedTimestamp json.RawMessage `json:"observedTimestamp"`
	TimeUnixNano      json.RawMessage `json:"timeUnixNano"`
	Body              flexStr         `json:"body"`
	AltBody           flexStr         `json:"_body"`

	Attributes map[string]any `json:"attributes"`
}

// Record-class ranks mirroring upstream CopilotUsageSource: chat spans win over
// inference logs, agent-turn logs, and agent summary spans, in that order.
const (
	chatSpan = iota
	inferenceLog
	agentTurnLog
	agentSummarySpan
)

// modelAttrs lists the model-identifying attributes in upstream precedence.
var modelAttrs = []string{"gen_ai.response.model", "gen_ai.request.model"}

// sessionAttrs ranks session-id-bearing attributes the way upstream does:
// the highest rank wins, and later keys win rank ties.
var sessionAttrs = []struct {
	key  string
	rank int
}{
	{"gen_ai.conversation.id", 3},
	{"copilot_chat.session_id", 3},
	{"copilot_chat.chat_session_id", 3},
	{"session.id", 3},
	{"github.copilot.interaction_id", 2},
	{"gen_ai.response.id", 1},
}

// traceContext carries the model and session ids collected from earlier
// records sharing one trace, used when a record names neither itself.
type traceContext struct {
	model   string
	session string
	rank    int
}

// usageCandidate is one record's usage before the cross-record suppression pass.
type usageCandidate struct {
	source     int
	traceID    string
	responseID string
	entry      model.Entry
}

// parseFile parses one OTel JSONL file into usage entries. Only records whose
// attributes carry gen_ai usage counters produce entries; lower-priority record
// classes that describe a response already covered by a higher-priority record
// (same trace or response id) are suppressed, mirroring upstream
// parse_otel_file/should_emit_candidate.
func parseFile(path string) []model.Entry {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var records []*record
	for _, line := range bytes.Split(content, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.Contains(line, []byte(`"attributes"`)) {
			continue
		}
		var rec record
		if json.Unmarshal(line, &rec) != nil || rec.Attributes == nil {
			continue
		}
		records = append(records, &rec)
	}
	contexts := collectTraceContexts(records)
	fallbackMs := fileModMillis(path)
	var candidates []usageCandidate
	for index, rec := range records {
		source, ok := classifyRecord(rec)
		if !ok {
			continue
		}
		c, ok := recordToCandidate(rec, source, index, fallbackMs, contexts)
		if !ok {
			continue
		}
		candidates = append(candidates, c)
	}
	return suppress(candidates)
}

// collectTraceContexts records, per trace id, the first model seen and the
// highest-ranked session id announced by any record on that trace.
func collectTraceContexts(records []*record) map[string]*traceContext {
	contexts := map[string]*traceContext{}
	for _, rec := range records {
		traceID, ok := traceIDOf(rec)
		if !ok || rec.Attributes == nil {
			continue
		}
		ctx, seen := contexts[traceID]
		if !seen {
			ctx = &traceContext{}
			contexts[traceID] = ctx
		}
		if ctx.model == "" {
			ctx.model = firstAttr(rec.Attributes, modelAttrs)
		}
		if session, rank, found := bestSessionAttr(rec.Attributes); found && rank > ctx.rank {
			ctx.session, ctx.rank = session, rank
		}
	}
	return contexts
}

// classifyRecord maps one record onto its usage class, ok=false when it is not
// a consumable usage shape (upstream is_chat_span_record and siblings).
func classifyRecord(rec *record) (int, bool) {
	attrs := rec.Attributes
	span := isSpanRecord(rec)
	name, _ := rec.Name.clean()
	body, hasBody := bodyOf(rec)
	switch {
	case span && (attrString(attrs, "gen_ai.operation.name") == "chat" ||
		strings.HasPrefix(name, "chat ")):
		return chatSpan, true
	case !span && (attrString(attrs, "event.name") == "gen_ai.client.inference.operation.details" ||
		(hasBody && strings.HasPrefix(body, "GenAI inference:"))):
		return inferenceLog, true
	case !span && (attrString(attrs, "event.name") == "copilot_chat.agent.turn" ||
		(hasBody && strings.HasPrefix(body, "copilot_chat.agent.turn"))):
		return agentTurnLog, true
	case span && (attrString(attrs, "gen_ai.operation.name") == "invoke_agent" ||
		strings.HasPrefix(name, "invoke_agent ")):
		return agentSummarySpan, true
	default:
		return 0, false
	}
}

// isSpanRecord mirrors upstream is_span_record: an explicit "type":"span"
// wins (compared exactly against the raw, untrimmed string), otherwise a
// named record carrying span coordinates counts as a span.
func isSpanRecord(rec *record) bool {
	if t, ok := rec.Type.raw(); ok {
		return t == "span"
	}
	if _, ok := rec.Name.clean(); !ok {
		return false
	}
	spanID, _ := rec.SpanID.clean()
	traceID, _ := rec.TraceID.clean()
	return spanID != "" || traceID != "" ||
		rawPresent(rec.StartTime) || rawPresent(rec.EndTime) ||
		rawPresent(rec.Duration) || rawPresent(rec.Kind)
}

// rawPresent reports whether a raw field carries a usable JSON value (present
// and not literal null), matching serde's Option<Value> decode.
func rawPresent(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null"))
}

func bodyOf(rec *record) (string, bool) {
	if b, ok := rec.Body.clean(); ok {
		return b, true
	}
	return rec.AltBody.clean()
}

// traceIDOf resolves the trace id from the flat field or the spanContext
// object; spanIDOf does the same for the span id.
func traceIDOf(rec *record) (string, bool) {
	if v, ok := rec.TraceID.clean(); ok {
		return v, true
	}
	return nestedString(rec.SpanContext, "traceId")
}

func spanIDOf(rec *record) (string, bool) {
	if v, ok := rec.SpanID.clean(); ok {
		return v, true
	}
	return nestedString(rec.SpanContext, "spanId")
}

func nestedString(obj map[string]any, key string) (string, bool) {
	if obj == nil {
		return "", false
	}
	s, ok := obj[key].(string)
	if !ok {
		return "", false
	}
	return flexStr(s).clean()
}

// recordToCandidate maps one record's gen_ai attributes onto a usage candidate.
// ok is false when the record carries no usage at all.
func recordToCandidate(rec *record, source, index int, fallbackMs int64, contexts map[string]*traceContext) (usageCandidate, bool) {
	attrs := rec.Attributes
	input := attrNumber(attrs, "gen_ai.usage.input_tokens")
	output := attrNumber(attrs, "gen_ai.usage.output_tokens")
	cacheRead := attrNumber(attrs,
		"gen_ai.usage.cache_read.input_tokens",
		"gen_ai.usage.cache_read_tokens")
	cacheCreate := attrNumber(attrs,
		"gen_ai.usage.cache_write.input_tokens",
		"gen_ai.usage.cache_creation.input_tokens")
	reasoning := attrNumber(attrs,
		"gen_ai.usage.reasoning.output_tokens",
		"gen_ai.usage.reasoning_tokens")
	total := attrNumber(attrs,
		"gen_ai.usage.total_tokens",
		"gen_ai.usage.total.token_count")

	// OpenAI-style input includes cached tokens: split them off like upstream
	// (input - min(input, cache_read)) before the total fallback.
	if cached := min64(cacheRead, input); cached > 0 {
		input -= cached
	}

	var e model.Entry
	applyTotalFallback(&e, reasoning, output, total)
	e.InputTokens = input
	e.CacheCreationTokens = cacheCreate
	e.CacheReadTokens = cacheRead
	if e.TotalTokens() == 0 {
		return usageCandidate{}, false
	}

	traceID, _ := traceIDOf(rec)
	var ctx *traceContext
	if traceID != "" {
		ctx = contexts[traceID]
	}
	modelName := firstNonEmpty(firstAttr(attrs, modelAttrs), ctxModel(ctx), "unknown")
	sessionID, _, found := bestSessionAttr(attrs)
	if !found && ctx != nil {
		sessionID = ctx.session
	}
	if sessionID == "" {
		sessionID = traceID
	}
	if sessionID == "" {
		sessionID = "unknown-session"
	}

	ms, ok := timestampOfRecord(rec)
	if !ok {
		ms = int64(fallbackMs)
	}

	e.Timestamp = timeFromMillis(ms)
	e.RawModel = modelName
	e.Model = modelName
	e.SessionID = sessionID
	e.Messages = 1
	e.Project = "copilot"
	e.ID = dedupKeyFor(source, rec, attrs, traceID, sessionID, ms, index)

	responseID := attrString(attrs, "gen_ai.response.id")
	return usageCandidate{source: source, traceID: traceID, responseID: responseID, entry: e}, true
}

func ctxModel(ctx *traceContext) string {
	if ctx == nil {
		return ""
	}
	return ctx.model
}

// bestSessionAttr returns the highest-ranked session attribute; later keys win
// rank ties (upstream max_by_key semantics).
func bestSessionAttr(attrs map[string]any) (string, int, bool) {
	best, bestRank, found := "", 0, false
	for _, sa := range sessionAttrs {
		if v := attrString(attrs, sa.key); v != "" {
			if !found || sa.rank >= bestRank {
				best, bestRank, found = v, sa.rank, true
			}
		}
	}
	return best, bestRank, found
}

func firstAttr(attrs map[string]any, keys []string) string {
	for _, k := range keys {
		if v := attrString(attrs, k); v != "" {
			return v
		}
	}
	return ""
}

// dedupKeyFor builds the entry identity upstream carries in message.id so the
// engine-level dedup collapses the same span across files.
func dedupKeyFor(source int, rec *record, attrs map[string]any, traceID, sessionID string, ms int64, index int) string {
	_, hasTrace := traceIDOf(rec)
	spanID, hasSpan := spanIDOf(rec)
	millis := strconv.FormatInt(ms, 10)
	pos := millis + ":" + strconv.Itoa(index)
	switch source {
	case chatSpan, agentSummarySpan:
		if hasTrace && hasSpan {
			return traceID + ":" + spanID
		}
		return "span:" + sessionID + ":" + pos
	case inferenceLog:
		if hasTrace && hasSpan {
			return "log:" + traceID + ":" + spanID
		}
		return "log:" + sessionID + ":" + pos
	default: // agentTurnLog
		turn := "idx-" + strconv.Itoa(index)
		if v, ok := attrCount(attrs, "turn.index"); ok {
			turn = strconv.FormatUint(v, 10)
		} else if v, ok := attrCount(attrs, "copilot_chat.turn.index"); ok {
			turn = strconv.FormatUint(v, 10)
		}
		if hasTrace {
			return "agent-turn:" + traceID + ":" + turn
		}
		return "agent-turn:" + sessionID + ":" + turn + ":" + strconv.Itoa(index)
	}
}

// suppress drops candidates whose trace or response id is already covered by a
// higher-priority record class, mirroring upstream should_emit_candidate. All
// candidates are registered before filtering, so the verdict is independent of
// file order.
func suppress(candidates []usageCandidate) []model.Entry {
	traces := [4]map[string]bool{}
	responses := [4]map[string]bool{}
	for _, c := range candidates {
		if traces[c.source] == nil {
			traces[c.source] = map[string]bool{}
			responses[c.source] = map[string]bool{}
		}
		if c.traceID != "" {
			traces[c.source][c.traceID] = true
		}
		if c.responseID != "" {
			responses[c.source][c.responseID] = true
		}
	}
	coveredByHigher := func(c usageCandidate) bool {
		for s := 0; s < c.source; s++ {
			if c.traceID != "" && traces[s][c.traceID] {
				return true
			}
			if c.responseID != "" && responses[s][c.responseID] {
				return true
			}
		}
		return false
	}
	var out []model.Entry
	for _, c := range candidates {
		if coveredByHigher(c) {
			continue
		}
		out = append(out, c.entry)
	}
	return out
}

// applyTotalFallback ports ccusage-core's apply_total_token_fallback for the
// copilot mapping: reasoning folds into output; a lone total backfills output
// and spills the remainder into ExtraTotalTokens.
func applyTotalFallback(e *model.Entry, reasoning, output, total uint64) {
	e.OutputTokens = output + reasoning
	if total == 0 {
		return
	}
	known := e.InputTokens + e.OutputTokens + e.CacheCreationTokens +
		e.CacheCreation1h + e.CacheReadTokens
	if known == 0 {
		e.OutputTokens = total
		return
	}
	if total > known {
		e.ExtraTotalTokens = total - known
	}
}

// timestampOfRecord resolves the record time the way upstream does:
// [seconds, nanos] arrays on endTime/startTime/hrTime/_hrTime/time first, then
// the scalar timestamp/observedTimestamp heuristics, then timeUnixNano nanos.
func timestampOfRecord(rec *record) (int64, bool) {
	for _, raw := range []json.RawMessage{rec.EndTime, rec.StartTime, rec.HrTime, rec.AltHrTime, rec.Time} {
		if ms, ok := timestampFromParts(raw); ok {
			return ms, true
		}
	}
	for _, raw := range []json.RawMessage{rec.Timestamp, rec.ObservedTimestamp} {
		if ms, ok := timestampFromScalar(raw); ok {
			return ms, true
		}
	}
	if n := jsonx.U64Of(rawAny(rec.TimeUnixNano)); n > 0 {
		return int64(n / 1_000_000), true
	}
	return 0, false
}

// timestampFromParts decodes an [seconds, nanos] pair into epoch millis.
func timestampFromParts(raw json.RawMessage) (int64, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '[' {
		return 0, false
	}
	var parts []float64
	if json.Unmarshal(t, &parts) != nil || len(parts) < 2 {
		return 0, false
	}
	sec, nano := parts[0], parts[1]
	if sec < 0 || nano < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) ||
		math.IsNaN(nano) || math.IsInf(nano, 0) {
		return 0, false
	}
	ms := sec*1_000 + math.Trunc(nano/1_000_000)
	if ms > math.MaxInt64 {
		ms = math.MaxInt64
	}
	return int64(ms), true
}

// timestampFromScalar applies upstream's magnitude heuristic: values above the
// nanosecond range divide down to millis, small values are epoch seconds.
func timestampFromScalar(raw json.RawMessage) (int64, bool) {
	var n float64
	switch t := rawAny(raw).(type) {
	case float64:
		n = t
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		n = f
	default:
		return 0, false
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, false
	}
	var ms float64
	switch {
	case n >= 100_000_000_000_000_000: // nanoseconds
		ms = n / 1_000_000
	case n >= 100_000_000_000_000: // microseconds
		ms = n / 1_000
	case n >= 100_000_000_000: // milliseconds
		ms = n
	default: // seconds
		ms = n * 1_000
	}
	if ms > math.MaxInt64 {
		ms = math.MaxInt64
	}
	return int64(ms), true
}

// rawAny wraps raw JSON bytes so the jsonx value helpers can read them.
func rawAny(raw json.RawMessage) any {
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func attrNumber(attrs map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		if raw, ok := attrs[k]; ok && raw != nil {
			if u := jsonx.U64Of(raw); u > 0 {
				return u
			}
		}
	}
	return 0
}

// attrCount reads a counter attribute, distinguishing "present" (including an
// explicit zero) from "absent" — upstream number_value semantics.
func attrCount(attrs map[string]any, key string) (uint64, bool) {
	raw, ok := attrs[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch n := raw.(type) {
	case float64:
		if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return uint64(n), true
	case string:
		u, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0, false
		}
		return u, true
	default:
		return 0, false
	}
}

func attrString(attrs map[string]any, key string) string {
	s, _ := attrs[key].(string)
	return strings.TrimSpace(s)
}

func fileModMillis(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UnixMilli()
	}
	return 0
}
