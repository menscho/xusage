package zed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/adapter"
	"xusage/internal/sources/jsonx"
)

// parseFile decodes one exported thread document into entries. The document is
// best-effort: a whole-file JSON array of turns, a single turn object, or
// newline-delimited JSON turns are all accepted. Malformed lines are skipped;
// only an unreadable file returns an error.
//
// A turn is any object carrying a model plus a usage/tokens block. The block
// may sit under "usage" or "tokens", or lie flat on the turn itself. Token
// keys accept the common spellings (input_tokens/input/prompt, output_tokens/
// output, cache_read_input_tokens/cache_read/cached, etc.). Cached tokens
// overlap the prompt, so the cache-read portion is carved back out of input.
func parseFile(path string) ([]model.Entry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &zedParser{
		fileSession: strings.TrimSuffix(filepath.Base(path), filepath.Ext(filepath.Base(path))),
		fallbackMs:  modTimeMs(path),
	}
	// Whole-file shapes: a single turn, an array of turns, or a {turns:[...]}
	// envelope. jsonx already gives us nested-object/array accessors.
	var doc any
	if json.Unmarshal(content, &doc) == nil {
		switch v := doc.(type) {
		case map[string]any:
			if turns := jsonx.Arr(v, "turns"); len(turns) > 0 {
				p.turns(turns)
			} else if looksLikeTurn(v) {
				p.turn(v)
			}
			return p.entries, nil
		case []any:
			p.turns(v)
			return p.entries, nil
		}
	}
	// Fallback: newline-delimited JSON turns.
	jsonx.WalkJSONL(path, func(line []byte) bool {
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			return true
		}
		if looksLikeTurn(m) {
			p.turn(m)
		}
		return true
	})
	return p.entries, nil
}

// zedParser threads the latest session id and model across the turns of one
// document, mirroring how a thread's earlier turns may omit fields that later
// ones repeat.
type zedParser struct {
	fileSession string
	fallbackMs  int64

	session string
	model   string
	entries []model.Entry
}

func (p *zedParser) turns(arr []any) {
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if ok {
			p.turn(m)
		}
	}
}

// turn folds one turn object into an entry, appending it.
func (p *zedParser) turn(m map[string]any) {
	if sid := firstNonEmpty(jsonx.Str(m, "session_id"), jsonx.Str(m, "sessionId"), jsonx.Str(m, "thread_id")); sid != "" {
		p.session = sid
	}
	if mdl := jsonx.Str(m, "model"); mdl != "" {
		p.model = mdl
	}
	if p.model == "" {
		return
	}

	tok := extractTokens(m)
	if tok.empty() {
		return
	}
	input, cacheRead := splitCached(tok)
	tsMs := turnTimestampMs(m, p.fallbackMs)

	modelName := adapter.Intern(p.model)
	e := model.Entry{
		ID:                  turnID(m, p.fileSession),
		Timestamp:           timeFromMillis(tsMs),
		Model:               modelName,
		RawModel:            modelName,
		SessionID:           p.session,
		InputTokens:         input,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: tok.cacheCreate,
		OutputTokens:        tok.output,
		Messages:            1,
	}
	if total := tok.total; total > 0 {
		applyTotalFallback(&e, total)
	}
	if cost := turnCost(m); cost != nil {
		e.ReportedCost = cost
	}
	p.entries = append(p.entries, e)
}

// zedTokens collects the many counter spellings into one struct.
type zedTokens struct {
	input       uint64
	output      uint64
	cacheRead   uint64
	cacheCreate uint64
	total       uint64
}

func (t zedTokens) empty() bool {
	return t.input == 0 && t.output == 0 && t.cacheRead == 0 &&
		t.cacheCreate == 0 && t.total == 0
}

// extractTokens pulls a token block out of a turn, looking under "usage"/
// "tokens" first, then falling back to flat fields on the turn itself.
func extractTokens(m map[string]any) zedTokens {
	obj := jsonx.Obj(m, "usage")
	if obj == nil {
		obj = jsonx.Obj(m, "tokens")
	}
	if obj == nil {
		obj = m
	}
	return zedTokens{
		input:       firstOf(obj, "input_tokens", "input", "prompt_tokens", "prompt"),
		output:      firstOf(obj, "output_tokens", "output", "completion_tokens"),
		cacheRead:   firstOf(obj, "cache_read_input_tokens", "cache_read", "cached_tokens", "cached"),
		cacheCreate: firstOf(obj, "cache_creation_input_tokens", "cache_creation", "cache_write"),
		total:       firstOf(obj, "total_tokens", "total"),
	}
}

// splitCached ports the Claude-shape cache semantics: cached tokens are a
// subset of the prompt, so they are carved back out of input and reported as
// cache-read.
func splitCached(t zedTokens) (input, cacheRead uint64) {
	cacheRead = min64(t.cacheRead, t.input)
	return t.input - cacheRead, cacheRead
}

// applyTotalFallback ports ccusage-core's apply_total_token_fallback for the
// Zed mapping: a provider-reported total that exceeds the known buckets spills
// into ExtraTotalTokens (or backfills a zero output bucket first).
func applyTotalFallback(e *model.Entry, total uint64) {
	known := e.InputTokens + e.OutputTokens + e.CacheCreationTokens +
		e.CacheReadTokens + e.ExtraTotalTokens
	if total <= known {
		return
	}
	missing := total - known
	if e.OutputTokens == 0 {
		e.OutputTokens = missing
		return
	}
	e.ExtraTotalTokens += missing
}

// turnTimestampMs resolves a turn's timestamp, falling back to the file mtime.
func turnTimestampMs(m map[string]any, fallback int64) int64 {
	for _, k := range []string{"timestamp", "created_at", "createdAt", "started_at", "time"} {
		if ms, ok := jsonx.Time(m[k]); ok {
			return ms
		}
	}
	return fallback
}

// turnID builds a stable dedup id for a turn: an explicit id wins, else the
// session id + timestamp keeps repeated turns distinct without colliding.
func turnID(m map[string]any, fileSession string) string {
	if id := firstNonEmpty(jsonx.Str(m, "id"), jsonx.Str(m, "message_id"), jsonx.Str(m, "request_id")); id != "" {
		return id
	}
	return fileSession + "#" + jsonx.Str(m, "timestamp")
}

// looksLikeTurn reports whether an object carries the fields this adapter
// consumes (a model plus any token counter), keeping unrelated JSON out.
func looksLikeTurn(m map[string]any) bool {
	if _, ok := m["model"].(string); !ok {
		return false
	}
	for _, key := range []string{"usage", "tokens", "input_tokens", "output_tokens", "total_tokens"} {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

// turnCost extracts a provider-reported cost from a turn, looking under
// "cost"/"costUSD"/"cost_usd" on the turn and its usage block. nil means
// absent.
func turnCost(m map[string]any) *float64 {
	keys := []string{"cost", "costUSD", "cost_usd"}
	candidates := []map[string]any{m, jsonx.Obj(m, "usage"), jsonx.Obj(m, "tokens")}
	for _, obj := range candidates {
		if obj == nil {
			continue
		}
		for _, k := range keys {
			if n, ok := obj[k].(float64); ok && n >= 0 {
				return &n
			}
		}
	}
	return nil
}

func firstOf(o map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		if u := jsonx.U64Of(o[k]); u > 0 {
			return u
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func timeFromMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func modTimeMs(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UnixMilli()
	}
	return 0
}
