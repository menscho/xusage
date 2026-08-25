package cline

import (
	"encoding/json"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/adapter"
	"xusage/internal/sources/jsonx"
)

// parseFile reads one Cline session transcript (newline-delimited JSON, one
// message/usage record per line) into entries. Malformed lines are skipped;
// only an unreadable file yields an error.
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

// parseRecord maps one Cline transcript record onto a model.Entry. Cline
// transcripts carry a "message" envelope whose "usage" holds the Claude-style
// token buckets; input and cache-read are reported as disjoint counts (no
// carving). ok is false when the record carries no usable usage.
func parseRecord(rec map[string]any) (model.Entry, bool) {
	msg := jsonx.Obj(rec, "message")
	if msg == nil {
		return model.Entry{}, false
	}
	usage := jsonx.Obj(msg, "usage")
	if usage == nil {
		return model.Entry{}, false
	}
	rawModel := jsonx.Str(msg, "model")
	if rawModel == "" {
		return model.Entry{}, false
	}
	ts, ok := jsonx.Time(rec["timestamp"])
	if !ok {
		return model.Entry{}, false
	}

	input := firstU64(usage, "input_tokens", "prompt_tokens")
	output := firstU64(usage, "output_tokens", "completion_tokens")
	cacheCreate := firstU64(usage, "cache_creation_input_tokens")
	cacheRead := firstU64(usage, "cache_read_input_tokens")

	modelName := adapter.Intern(rawModel)
	e := model.Entry{
		ID:                  jsonx.Str(msg, "id"),
		Timestamp:           time.UnixMilli(ts).UTC(),
		Model:               modelName,
		RawModel:            modelName,
		SessionID:           firstStr(rec, "session_id", "sessionId"),
		Project:             firstStr(rec, "project"),
		InputTokens:         input,
		OutputTokens:        output,
		CacheCreationTokens: cacheCreate,
		CacheReadTokens:     cacheRead,
		Messages:            1,
	}
	if cost, ok := firstF64Ptr(rec, "costUSD", "cost", "totalCost"); ok {
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
