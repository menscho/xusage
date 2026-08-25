package qwen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/adapter"
	"xusage/internal/sources/jsonx"
)

// qwenLine mirrors one Qwen chat record (subset consumed by xusage).
type qwenLine struct {
	Type          string          `json:"type"`
	Timestamp     string          `json:"timestamp"`
	SessionID     string          `json:"sessionId"`
	Model         string          `json:"model"`
	UsageMetadata json.RawMessage `json:"usageMetadata"`
}

// usageMetadata carries Gemini-style counters.
type usageMetadata struct {
	PromptTokenCount        uint64 `json:"promptTokenCount"`
	CandidatesTokenCount    uint64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      uint64 `json:"thoughtsTokenCount"`
	CachedContentTokenCount uint64 `json:"cachedContentTokenCount"`
	TotalTokenCount         uint64 `json:"totalTokenCount"`
}

// parseFile parses one Qwen chat JSONL file into entries.
func parseFile(path string) []model.Entry {
	project := projectFromFile(path)
	if project == "" {
		project = "unknown"
	}
	stem := filepath.Base(path)
	stem = strings.TrimSuffix(stem, filepath.Ext(stem))

	var entries []model.Entry
	err := jsonx.WalkJSONL(path, func(line []byte) bool {
		var rec qwenLine
		if json.Unmarshal(line, &rec) != nil {
			return true // malformed lines are skipped
		}
		if rec.Type != "assistant" {
			return true
		}
		if len(rec.UsageMetadata) == 0 || bytes.Equal(rec.UsageMetadata, []byte("null")) {
			return true
		}
		var u usageMetadata
		if json.Unmarshal(rec.UsageMetadata, &u) != nil {
			return true
		}
		input := u.PromptTokenCount
		output := u.CandidatesTokenCount
		cacheRead := min64(u.CachedContentTokenCount, input)
		reasoning := u.ThoughtsTokenCount
		m := adapter.Intern(rec.Model)
		e := model.Entry{
			Timestamp:       timestampOf(rec.Timestamp, path),
			RawModel:        m,
			Model:           m,
			Project:         project,
			InputTokens:     input,
			OutputTokens:    output,
			CacheReadTokens: cacheRead,
			Messages:        1,
		}
		if m == "" {
			e.RawModel = "unknown"
			e.Model = "unknown"
		}
		applyTotalFallback(&e, reasoning, u.TotalTokenCount)
		if e.TotalTokens() == 0 && e.ExtraTotalTokens == 0 {
			return true // no usable usage in the record
		}
		// Session id falls back to "<project>-<stem>" like upstream, keeping
		// files without an explicit sessionId distinct per chat.
		e.SessionID = rec.SessionID
		if e.SessionID == "" {
			e.SessionID = project + "-" + stem
		}
		entries = append(entries, e)
		return true
	})
	if err != nil {
		return nil
	}
	return entries
}

// timestampOf parses the record timestamp; a missing or invalid stamp falls
// back to the file's modification time so entries still land on a day.
func timestampOf(ts, path string) time.Time {
	if t, ok := parseRFC3339(ts); ok {
		return t.UTC()
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UTC()
	}
	return time.Unix(0, 0).UTC()
}

// parseRFC3339 parses an RFC3339-shaped timestamp the way upstream
// parse_ts_timestamp does, returning the time and whether it parsed. Trimming
// once up front avoids the previous double trim+parse per record.
func parseRFC3339(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// applyTotalFallback ports ccusage-core's apply_total_token_fallback exactly:
// reasoning tokens are unpriced metadata and start life in ExtraTotalTokens;
// a grand total above every known bucket backfills zero output first, then
// spills the remainder into ExtraTotalTokens.
func applyTotalFallback(e *model.Entry, reasoning, total uint64) {
	e.ExtraTotalTokens = reasoning
	known := e.InputTokens + e.OutputTokens + e.CacheCreationTokens +
		e.CacheCreation1h + e.CacheReadTokens + e.ExtraTotalTokens
	if total <= known {
		return
	}
	missing := total - known
	if e.OutputTokens == 0 {
		e.OutputTokens = missing
	} else {
		e.ExtraTotalTokens += missing
	}
}
