package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/jsonx"
)

// parseFile parses one Gemini log document (a single JSON object or JSONL
// lines of the same shape) into entries, mirroring upstream parse_json_file /
// parse_jsonl_file: a "messages" array short-circuits the document; a direct
// type=="gemini" record parses its own "tokens"; a stats block fans out per
// model under "stats.models".
func parseFile(path string) []model.Entry {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	p := &geminiParser{
		fileSession: strings.TrimSuffix(filepath.Base(path), filepath.Ext(filepath.Base(path))),
		fallbackMs:  fileModTime(path),
	}

	var doc any
	if json.Unmarshal(content, &doc) == nil {
		if m, isObj := doc.(map[string]any); isObj && looksLikeRecord(m) {
			return p.record(m)
		}
	}
	jsonx.WalkJSONL(path, func(line []byte) bool {
		var v any
		if json.Unmarshal(line, &v) != nil {
			return true
		}
		if m, ok := v.(map[string]any); ok && looksLikeRecord(m) {
			p.record(m)
		}
		return true
	})
	if len(p.entries) > 0 {
		return p.entries
	}
	// A whole-file array of records.
	if arr, ok := doc.([]any); ok {
		for _, item := range arr {
			if m, isObj := item.(map[string]any); isObj && looksLikeRecord(m) {
				p.record(m)
			}
		}
	}
	return p.entries
}

// looksLikeRecord reports whether an object carries any field this adapter
// consumes (tokens/stats/result/messages), keeping unrelated JSON files out.
func looksLikeRecord(m map[string]any) bool {
	for _, key := range []string{"tokens", "stats", "result", "messages"} {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

// inputNormalizer mirrors the two upstream input/cache strategies:
// overlapInput subtracts cached tokens from input (stats rows), while
// sessionInput keeps them separate unless the totals prove an inclusive count
// (per-message session rows).
type inputNormalizer func(t geminiTokens) (input, cacheRead uint64)

// overlapInput ports upstream subtract_cached_overlap_tokens: cached is a
// subset of input, so it is carved back out.
func overlapInput(t geminiTokens) (uint64, uint64) {
	cached := min64(t.cached, t.input)
	return t.input - cached, t.cached
}

// sessionInput ports upstream normalize_session_input: cached sits beside
// input unless total proves the cached part was already included in it.
func sessionInput(t geminiTokens) (uint64, uint64) {
	inclusive := t.input + t.output + t.thoughts + t.tool
	exclusive := inclusive + t.cached
	if t.cached > 0 && t.total != 0 && t.total == inclusive && t.total != exclusive {
		return overlapInput(t)
	}
	return t.input, t.cached
}

// geminiEvent pairs one model name with its token counters and the input
// normalization to apply for its record class.
type geminiEvent struct {
	model  string
	tokens geminiTokens
	norm   inputNormalizer
}

// msgTimestampMs resolves per-record timestamps: timestamp/createdAt first,
// startTime/lastUpdated as extra candidates before the file fallback.
func msgTimestampMs(m map[string]any, fallback int64) int64 {
	if ms, ok := timestampMs(m, "timestamp", "createdAt"); ok {
		return ms
	}
	if ms, ok := timestampMs(m, "startTime", "lastUpdated"); ok {
		return ms
	}
	return fallback
}

// geminiParser carries the JSONL-line state upstream threads through
// parse_jsonl_file: the latest session id and model, plus an id -> index map
// so a re-emitted direct event replaces its earlier copy instead of stacking up.
type geminiParser struct {
	fileSession string
	fallbackMs  int64

	session     string
	model       string
	directIndex map[string]int // message id -> entry index (direct events only)
	entries     []model.Entry
}

// record folds one log record into the parser state, appending its events to
// p.entries, and returns the delta for whole-document callers. JSONL mode
// ignores the delta and reads p.entries directly, so id-replacement stays
// visible across lines.
func (p *geminiParser) record(m map[string]any) []model.Entry {
	before := len(p.entries)
	if sid := firstNonEmpty(jsonx.Str(m, "sessionId"), jsonx.Str(m, "session_id")); sid != "" {
		p.session = sid
	}
	if mdl := jsonx.Str(m, "model"); mdl != "" {
		p.model = mdl
	}

	// A messages array short-circuits: only per-message events count. In
	// JSONL mode the array contributes exactly its own rows — earlier lines'
	// entries stay (upstream parse_jsonl_file appends per record).
	if msgs, isArr := m["messages"].([]any); isArr {
		saved, savedIndex := p.entries, p.directIndex
		p.entries, p.directIndex = nil, nil
		for _, item := range msgs {
			msg, isObj := item.(map[string]any)
			if !isObj {
				continue
			}
			if raw, ok := msg["type"].(string); !ok || raw != "gemini" { // exact, untrimmed
				continue
			}
			toks := jsonx.Obj(msg, "tokens")
			if toks == nil {
				continue
			}
			p.emit(geminiEvent{model: jsonx.Str(msg, "model"), tokens: parseTokens(toks), norm: sessionInput},
				msgTimestampMs(msg, p.fallbackMs))
		}
		fresh := p.entries
		p.entries, p.directIndex = saved, savedIndex
		p.entries = append(p.entries, fresh...)
		return fresh
	}

	ts := msgTimestampMs(m, p.fallbackMs)

	if raw, ok := m["type"].(string); ok && raw == "gemini" { // exact match, untrimmed
		if toks := jsonx.Obj(m, "tokens"); toks != nil {
			p.trackDirect(m, geminiEvent{model: p.model, tokens: parseTokens(toks), norm: sessionInput}, ts)
		}
		return p.entries[before:]
	}

	stats := jsonx.Obj(m, "stats")
	if stats == nil {
		if res := jsonx.Obj(m, "result"); res != nil {
			stats = jsonx.Obj(res, "stats")
		}
	}
	if stats != nil {
		p.statsEvents(stats, ts)
		return p.entries[before:]
	}

	// Lenient fallback (superset of upstream): a bare tokens block still
	// describes usage, so parse it rather than lose the record. Cached tokens
	// are treated as part of the prompt (overlap semantics), matching the
	// stats path.
	toks := jsonx.Obj(m, "tokens")
	if toks == nil {
		if res := jsonx.Obj(m, "result"); res != nil {
			toks = jsonx.Obj(res, "tokens")
		}
	}
	if toks != nil {
		p.emit(geminiEvent{model: p.model, tokens: parseTokens(toks), norm: overlapInput}, ts)
	}
	return p.entries[before:]
}

// trackDirect appends a direct event; when the record carries an id already
// seen, the new event replaces the earlier copy instead of stacking up.
func (p *geminiParser) trackDirect(m map[string]any, ev geminiEvent, ms int64) {
	id := jsonx.Str(m, "id")
	if id == "" {
		p.emit(ev, ms)
		return
	}
	e, ok := buildEntry(ev, ms, p.currentSession())
	if !ok {
		return
	}
	if p.directIndex == nil {
		p.directIndex = map[string]int{}
	}
	if idx, dup := p.directIndex[id]; dup {
		p.entries[idx] = e
		return
	}
	p.directIndex[id] = len(p.entries)
	p.entries = append(p.entries, e)
}

// statsEvents mirrors upstream parse_stats_events: per-model rows win, the
// stats object itself is the fallback token block.
func (p *geminiParser) statsEvents(stats map[string]any, ms int64) {
	models := jsonx.Obj(stats, "models")
	if len(models) > 0 {
		names := make([]string, 0, len(models))
		for name := range models {
			names = append(names, name)
		}
		sortStrings(names)
		emitted := false
		for _, name := range names {
			data := jsonx.Obj(models, name)
			if data == nil {
				continue
			}
			toks := jsonx.Obj(data, "tokens")
			if toks == nil {
				toks = data // lenient: counters may sit directly on the model row
			}
			p.emit(geminiEvent{model: name, tokens: parseTokens(toks), norm: overlapInput}, ms)
			emitted = true
		}
		if emitted {
			return
		}
	}
	if t := parseTokens(stats); !t.empty() {
		p.emit(geminiEvent{model: p.model, tokens: t, norm: overlapInput}, ms)
	}
}

// emit turns one event into an Entry and appends it.
func (p *geminiParser) emit(ev geminiEvent, ms int64) {
	e, ok := buildEntry(ev, ms, p.currentSession())
	if ok {
		p.entries = append(p.entries, e)
	}
}

func (p *geminiParser) currentSession() string {
	if p.session != "" {
		return p.session
	}
	return p.fileSession
}

// buildEntry turns one per-model token block into an Entry. ok is false when
// the model name is empty or every bucket is zero after normalization.
func buildEntry(ev geminiEvent, ms int64, sessionID string) (model.Entry, bool) {
	t := ev.tokens
	modelName := firstNonEmpty(ev.model, defaultModelName)
	if modelName == "" {
		return model.Entry{}, false // no usable model name anywhere
	}

	input, cacheRead := ev.norm(t)
	input += t.tool // tool-call tokens are billable input (upstream build_event)

	e := model.Entry{
		Timestamp:       timeFromMillis(ms),
		RawModel:        modelName,
		Model:           modelName,
		SessionID:       sessionID,
		InputTokens:     input,
		CacheReadTokens: cacheRead,
		Messages:        1,
	}
	applyTotalFallback(&e, t.thoughts, t.output, t.total)
	if e.TotalTokens() == 0 {
		return model.Entry{}, false
	}
	e.Project = "gemini"
	return e, true
}

const defaultModelName = "unknown"

// geminiTokens collects the many counter spellings into one struct.
type geminiTokens struct {
	input    uint64
	output   uint64
	cached   uint64
	thoughts uint64
	tool     uint64
	total    uint64 // grand total; 0 = absent
}

func (t geminiTokens) empty() bool {
	return t.input == 0 && t.output == 0 && t.cached == 0 &&
		t.thoughts == 0 && t.tool == 0 && t.total == 0
}

// sortStrings sorts names in place so per-model fan-out is deterministic.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func parseTokens(o map[string]any) geminiTokens {
	t := geminiTokens{
		input:    firstOf(o, "input", "input_tokens", "prompt", "prompt_tokens"),
		output:   firstOf(o, "output", "output_tokens", "candidates", "candidates_tokens"),
		cached:   firstOf(o, "cached", "cached_tokens"),
		thoughts: firstOf(o, "thoughts", "thoughts_tokens", "thinking", "reasoning"),
		tool:     firstOf(o, "tool", "tool_tokens"),
	}
	t.total = firstOf(o, "total", "total_tokens")
	return t
}

func firstOf(o map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		if raw, ok := o[k]; ok && raw != nil {
			if u := jsonx.U64Of(raw); u > 0 {
				return u
			}
		}
	}
	return 0
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// applyTotalFallback ports ccusage-core's apply_total_token_fallback for the
// gemini mapping: thoughts fold into output; a lone total backfills output and
// spills the remainder into ExtraTotalTokens.
func applyTotalFallback(e *model.Entry, thoughts, output, total uint64) {
	e.OutputTokens = output + thoughts
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

// timestampMs tries each key in order until one parses to a usable time.
func timestampMs(m map[string]any, keys ...string) (int64, bool) {
	for _, k := range keys {
		if ms, ok := jsonx.Time(m[k]); ok {
			return ms, true
		}
	}
	return 0, false
}

func fileModTime(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UnixMilli()
	}
	return 0
}

func timeFromMillis(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
