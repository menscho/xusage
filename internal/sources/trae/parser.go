package trae

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

// parseFile decodes one Trae usage-cache document into entries. The document
// is best-effort: a whole-file JSON array of records, a {records}/{items}
// envelope, a single record object, or newline-delimited JSON records are all
// accepted. Malformed lines are skipped; only an unreadable file returns an
// error.
//
// A record is any object carrying a model plus a token block. The block may sit
// under "usage"/"tokens"/"token_usage", or lie flat on the record. Token keys
// accept the common spellings. Cached tokens overlap the prompt, so the cache-
// read portion is carved back out of input; a provider-reported total that
// exceeds the known buckets spills into ExtraTotalTokens.
func parseFile(path string) ([]model.Entry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &traeParser{
		fileSession: strings.TrimSuffix(filepath.Base(path), filepath.Ext(filepath.Base(path))),
		fallbackMs:  modTimeMs(path),
	}
	var doc any
	if json.Unmarshal(content, &doc) == nil {
		switch v := doc.(type) {
		case map[string]any:
			if arr := recordArray(v); len(arr) > 0 {
				p.records(arr)
				return p.entries, nil
			}
			if looksLikeRecord(v) {
				p.record(v)
			}
			return p.entries, nil
		case []any:
			p.records(v)
			return p.entries, nil
		}
	}
	jsonx.WalkJSONL(path, func(line []byte) bool {
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			return true
		}
		if looksLikeRecord(m) {
			p.record(m)
		}
		return true
	})
	return p.entries, nil
}

// recordArray returns the first array-valued envelope key ("records", "items",
// "data", "usage") on an object, or nil when none is present.
func recordArray(m map[string]any) []any {
	for _, k := range []string{"records", "items", "data", "usage"} {
		if arr := jsonx.Arr(m, k); len(arr) > 0 {
			return arr
		}
	}
	return nil
}

type traeParser struct {
	fileSession string
	fallbackMs  int64

	model   string
	entries []model.Entry
}

func (p *traeParser) records(arr []any) {
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if ok {
			p.record(m)
		}
	}
}

func (p *traeParser) record(m map[string]any) {
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
	tsMs := recordTimestampMs(m, p.fallbackMs)

	modelName := adapter.Intern(p.model)
	e := model.Entry{
		ID:                  recordID(m, p.fileSession),
		Timestamp:           timeFromMillis(tsMs),
		Model:               modelName,
		RawModel:            modelName,
		InputTokens:         input,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: tok.cacheCreate,
		OutputTokens:        tok.output,
		Messages:            1,
	}
	if total := tok.total; total > 0 {
		applyTotalFallback(&e, total)
	}
	if cost := recordCost(m); cost != nil {
		e.ReportedCost = cost
	}
	p.entries = append(p.entries, e)
}

type traeTokens struct {
	input       uint64
	output      uint64
	cacheRead   uint64
	cacheCreate uint64
	total       uint64
}

func (t traeTokens) empty() bool {
	return t.input == 0 && t.output == 0 && t.cacheRead == 0 &&
		t.cacheCreate == 0 && t.total == 0
}

// extractTokens pulls a token block out of a record, looking under "usage"/
// "tokens"/"token_usage" first, then falling back to flat fields.
func extractTokens(m map[string]any) traeTokens {
	obj := jsonx.Obj(m, "usage")
	if obj == nil {
		obj = jsonx.Obj(m, "tokens")
	}
	if obj == nil {
		obj = jsonx.Obj(m, "token_usage")
	}
	if obj == nil {
		obj = m
	}
	return traeTokens{
		input:       firstOf(obj, "input_tokens", "input", "prompt_tokens", "prompt"),
		output:      firstOf(obj, "output_tokens", "output", "completion_tokens"),
		cacheRead:   firstOf(obj, "cache_read_input_tokens", "cache_read", "cached_tokens", "cached"),
		cacheCreate: firstOf(obj, "cache_creation_input_tokens", "cache_creation", "cache_write"),
		total:       firstOf(obj, "total_tokens", "total"),
	}
}

func splitCached(t traeTokens) (input, cacheRead uint64) {
	cacheRead = min64(t.cacheRead, t.input)
	return t.input - cacheRead, cacheRead
}

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

func recordTimestampMs(m map[string]any, fallback int64) int64 {
	for _, k := range []string{"timestamp", "created_at", "createdAt", "started_at", "time", "ts"} {
		if ms, ok := jsonx.Time(m[k]); ok {
			return ms
		}
	}
	return fallback
}

func recordID(m map[string]any, fileSession string) string {
	if id := firstNonEmpty(jsonx.Str(m, "id"), jsonx.Str(m, "request_id"), jsonx.Str(m, "message_id")); id != "" {
		return id
	}
	return fileSession + "#" + jsonx.Str(m, "timestamp")
}

func recordCost(m map[string]any) *float64 {
	keys := []string{"cost", "costUSD", "cost_usd"}
	candidates := []map[string]any{m, jsonx.Obj(m, "usage"), jsonx.Obj(m, "tokens"), jsonx.Obj(m, "token_usage")}
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

// looksLikeRecord reports whether an object carries a model plus any token
// counter, keeping unrelated JSON out.
func looksLikeRecord(m map[string]any) bool {
	if _, ok := m["model"].(string); !ok {
		return false
	}
	for _, key := range []string{"usage", "tokens", "token_usage", "input_tokens", "output_tokens", "total_tokens"} {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
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
