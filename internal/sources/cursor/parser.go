package cursor

import (
	"encoding/json"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/adapter"
	"xusage/internal/sources/jsonx"
)

// parseFile reads one Cursor usage-export file (newline-delimited JSON, one
// usage record per line) into entries. Malformed lines are skipped; only an
// unreadable file yields an error.
func parseFile(path string) ([]model.Entry, error) {
	var entries []model.Entry
	err := jsonx.WalkJSONL(path, func(line []byte) bool {
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil {
			return true
		}
		if e, ok := parseRecord(rec); ok {
			entries = append(entries, e)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// parseRecord maps one Cursor usage-export object onto a model.Entry. The
// export is OpenAI-style: cached tokens are reported as a subset of the input,
// so they are carved back out of input to keep the input and cache-read
// buckets disjoint (the dashboard hit-rate divides cache-read by their sum).
// ok is false when the record carries no usable usage.
func parseRecord(rec map[string]any) (model.Entry, bool) {
	rawModel := firstStr(rec, "model", "model_name", "modelName")
	if rawModel == "" {
		return model.Entry{}, false
	}
	ts, ok := jsonx.Time(rec["timestamp"])
	if !ok {
		return model.Entry{}, false
	}

	input := firstU64(rec, "input_tokens", "prompt_tokens", "input")
	output := firstU64(rec, "output_tokens", "completion_tokens", "output")
	cacheCreate := firstU64(rec, "cache_creation_input_tokens", "cache_creation")
	cacheRead := firstU64(rec, "cache_read_input_tokens", "cached_input_tokens", "cached_tokens")
	total := firstU64(rec, "total_tokens", "total")

	// Cached tokens are a subset of input; carve them out so the two buckets are
	// disjoint, mirroring upstream Codex's bucket split.
	cacheRead = minU64(cacheRead, input)
	input -= cacheRead

	var extra uint64
	if total > 0 && input == 0 && output == 0 && cacheCreate == 0 && cacheRead == 0 {
		// A lone grand total with no per-bucket split maps to ExtraTotalTokens
		// (SPEC §3.2).
		extra = total
	}

	modelName := adapter.Intern(rawModel)
	e := model.Entry{
		ID:                  firstStr(rec, "request_id", "requestId", "id"),
		Timestamp:           time.UnixMilli(ts).UTC(),
		Model:               modelName,
		RawModel:            modelName,
		SessionID:           firstStr(rec, "session_id", "sessionId"),
		Project:             firstStr(rec, "project"),
		InputTokens:         input,
		OutputTokens:        output,
		CacheCreationTokens: cacheCreate,
		CacheReadTokens:     cacheRead,
		ExtraTotalTokens:    extra,
		Messages:            1,
	}
	if cost, ok := firstF64Ptr(rec, "cost", "costUSD", "cost_usd"); ok {
		e.ReportedCost = cost
	}
	if e.TotalTokens() == 0 {
		return model.Entry{}, false
	}
	return e, true
}

// firstU64 returns the first positive numeric field among keys, or 0.
func firstU64(m map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		if v := jsonx.U64(m, k); v > 0 {
			return v
		}
	}
	return 0
}

// firstStr returns the first non-empty string field among keys, or "".
func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := jsonx.Str(m, k); s != "" {
			return s
		}
	}
	return ""
}

// firstF64Ptr returns the first present float64 field among keys as a pointer.
func firstF64Ptr(m map[string]any, keys ...string) (*float64, bool) {
	for _, k := range keys {
		if f, ok := jsonx.F64Ptr(m, k); ok {
			return &f, true
		}
	}
	return nil, false
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
