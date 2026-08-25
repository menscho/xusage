package augment

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

// parseFile decodes one Augment session document into entries. A session is a
// JSON object carrying a model, a timestamp, and a token block. The block may
// sit under "usage"/"tokens" or lie flat on the session. Token keys accept the
// common spellings. Malformed input is skipped; only an unreadable file returns
// an error.
//
// Cached tokens overlap the prompt, so the cache-read portion is carved back
// out of input; a provider-reported total that exceeds the known buckets spills
// into ExtraTotalTokens.
func parseFile(path string) ([]model.Entry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fileSession := strings.TrimSuffix(filepath.Base(path), filepath.Ext(filepath.Base(path)))

	var m map[string]any
	if json.Unmarshal(content, &m) != nil {
		return nil, nil // not a JSON object: nothing to parse, not an error
	}
	if !looksLikeSession(m) {
		return nil, nil
	}

	modelName := adapter.Intern(firstNonEmpty(jsonx.Str(m, "model"), jsonx.Str(m, "model_id")))
	if modelName == "" {
		return nil, nil
	}
	tok := extractTokens(m)
	if tok.empty() {
		return nil, nil
	}
	input, cacheRead := splitCached(tok)
	tsMs := sessionTimestampMs(m, modTimeMs(path))

	e := model.Entry{
		ID:                  sessionID(m, fileSession),
		Timestamp:           timeFromMillis(tsMs),
		Model:               modelName,
		RawModel:            modelName,
		SessionID:           firstNonEmpty(jsonx.Str(m, "session_id"), jsonx.Str(m, "sessionId"), fileSession),
		InputTokens:         input,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: tok.cacheCreate,
		OutputTokens:        tok.output,
		Messages:            1,
	}
	if total := tok.total; total > 0 {
		applyTotalFallback(&e, total)
	}
	if cost := sessionCost(m); cost != nil {
		e.ReportedCost = cost
	}
	return []model.Entry{e}, nil
}

type augmentTokens struct {
	input       uint64
	output      uint64
	cacheRead   uint64
	cacheCreate uint64
	total       uint64
}

func (t augmentTokens) empty() bool {
	return t.input == 0 && t.output == 0 && t.cacheRead == 0 &&
		t.cacheCreate == 0 && t.total == 0
}

// extractTokens pulls a token block out of a session, looking under "usage"/
// "tokens" first, then falling back to flat fields.
func extractTokens(m map[string]any) augmentTokens {
	obj := jsonx.Obj(m, "usage")
	if obj == nil {
		obj = jsonx.Obj(m, "tokens")
	}
	if obj == nil {
		obj = m
	}
	return augmentTokens{
		input:       firstOf(obj, "input_tokens", "input", "prompt_tokens", "prompt"),
		output:      firstOf(obj, "output_tokens", "output", "completion_tokens"),
		cacheRead:   firstOf(obj, "cache_read_input_tokens", "cache_read", "cached_tokens", "cached"),
		cacheCreate: firstOf(obj, "cache_creation_input_tokens", "cache_creation", "cache_write"),
		total:       firstOf(obj, "total_tokens", "total"),
	}
}

func splitCached(t augmentTokens) (input, cacheRead uint64) {
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

func sessionTimestampMs(m map[string]any, fallback int64) int64 {
	for _, k := range []string{"timestamp", "created_at", "createdAt", "started_at", "updated_at", "time"} {
		if ms, ok := jsonx.Time(m[k]); ok {
			return ms
		}
	}
	return fallback
}

func sessionID(m map[string]any, fileSession string) string {
	return firstNonEmpty(jsonx.Str(m, "id"), jsonx.Str(m, "session_id"), jsonx.Str(m, "sessionId"), fileSession)
}

func sessionCost(m map[string]any) *float64 {
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

// looksLikeSession reports whether an object carries a model plus any token
// counter, keeping unrelated JSON out.
func looksLikeSession(m map[string]any) bool {
	if _, ok := m["model"].(string); !ok {
		if _, ok := m["model_id"].(string); !ok {
			return false
		}
	}
	for _, key := range []string{"usage", "tokens", "input_tokens", "output_tokens", "total_tokens"} {
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
