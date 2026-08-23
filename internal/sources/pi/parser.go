package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"xusage/internal/model"
)

// piLine mirrors one JSONL record of a pi session file. Only the fields
// xusage consumes are declared; everything else is skipped.
type piLine struct {
	Type      flexString `json:"type"`
	Timestamp flexString `json:"timestamp"`
	Message   *piMessage `json:"message"`
}

// piMessage is the message block carried by assistant records.
type piMessage struct {
	Role  flexString `json:"role"`
	Model flexString `json:"model"`
	Usage *piUsage   `json:"usage"`
}

// piUsage holds token counts and an optional display cost block. Every scalar
// decodes leniently so an unexpectedly typed field never fails the record.
type piUsage struct {
	Input       flexU64         `json:"input"`
	Output      flexU64         `json:"output"`
	CacheRead   flexU64         `json:"cacheRead"`
	CacheWrite  flexU64         `json:"cacheWrite"`
	TotalTokens flexU64         `json:"totalTokens"`
	Cost        json.RawMessage `json:"cost"`
}

// flexU64 mirrors upstream's lenient_u64: JSON integers pass through, while
// floats, strings, booleans, arrays, objects, nulls, and negative or
// overflowing numbers become 0 instead of failing the record.
type flexU64 uint64

func (f *flexU64) UnmarshalJSON(data []byte) error {
	*f = 0
	// Only JSON numbers may carry a count: strings, booleans, nulls, arrays,
	// and objects all coerce to 0 like upstream's Value::as_u64.
	if len(data) == 0 || data[0] != '-' && (data[0] < '0' || data[0] > '9') {
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return nil
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || v < 0 {
		return nil
	}
	*f = flexU64(v)
	return nil
}

// flexString mirrors upstream's non_empty_string: strings are trimmed and
// empty-after-trim values become absent; any other JSON value also becomes
// absent instead of failing the record.
type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		*s = ""
		return nil
	}
	*s = flexString(strings.TrimSpace(str))
	return nil
}

// Parse extracts usage entries from one pi session file. Malformed lines are
// skipped; only unreadable files warrant an error.
func Parse(path string) ([]model.Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	project := extractProject(path)
	sessionID := extractSessionID(path)

	var entries []model.Entry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, `"usage"`) || !strings.Contains(line, `"message"`) {
			continue // fast prefilter: usable lines carry both keys
		}
		var rec piLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		e, ok := entryFromRecord(&rec)
		if !ok {
			continue
		}
		e.Project = project
		e.SessionID = sessionID
		entries = append(entries, e)
	}
	return entries, nil
}

// entryFromRecord converts one parsed record into a model.Entry. It reports
// false for records that carry no usable assistant usage.
func entryFromRecord(rec *piLine) (model.Entry, bool) {
	if rec.Type != "" && rec.Type != "message" {
		return model.Entry{}, false
	}
	msg := rec.Message
	if msg == nil || msg.Role != "assistant" || msg.Usage == nil {
		return model.Entry{}, false
	}
	ts, err := time.Parse(time.RFC3339, string(rec.Timestamp))
	if err != nil || ts.IsZero() {
		return model.Entry{}, false
	}

	u := msg.Usage
	e := model.Entry{
		Timestamp:           ts.UTC(),
		InputTokens:         uint64(u.Input),
		OutputTokens:        uint64(u.Output),
		CacheReadTokens:     uint64(u.CacheRead),
		CacheCreationTokens: uint64(u.CacheWrite),
		Messages:            1,
	}
	applyTotalTokenFallback(&e, uint64(u.TotalTokens))
	if e.TotalTokens() == 0 {
		return model.Entry{}, false // no tokens: not a usage event
	}
	if msg.Model != "" {
		e.RawModel = string(msg.Model)
		e.Model = string(msg.Model)
	}
	e.ReportedCost = costTotal(u.Cost)
	return e, true
}

// costTotal extracts cost.total from a raw cost payload. Only a JSON object
// carries a display cost (mirroring upstream lenient_object); bare numbers,
// strings, arrays, and nulls yield nil.
func costTotal(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var c struct {
		Total *float64 `json:"total"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil
	}
	return c.Total
}

// applyTotalTokenFallback folds a provider-reported grand total into the split
// buckets: missing tokens land in OutputTokens when it is still zero, and in
// ExtraTotalTokens otherwise (port of upstream apply_total_token_fallback).
func applyTotalTokenFallback(e *model.Entry, total uint64) {
	known := e.TotalTokens()
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

// extractSessionID derives the session id from the file name: the stem after
// the first underscore ("agent_<session>.jsonl"), or the whole stem.
func extractSessionID(path string) string {
	stem := filepath.Base(path)
	if ext := filepath.Ext(stem); ext != "" {
		stem = strings.TrimSuffix(stem, ext)
	}
	if _, rest, ok := strings.Cut(stem, "_"); ok {
		return rest
	}
	return stem
}

// extractProject returns the path segment immediately after the "sessions"
// component, or "unknown" when the layout does not match.
func extractProject(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		if part == "sessions" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return "unknown"
}
