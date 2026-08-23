package openclaw

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"xusage/internal/model"
)

// prefilterStrings are the substrings that admit a line for JSON parsing:
// model-tracking records and assistant messages carrying usage.
var prefilterStrings = []string{`"model_change"`, `"model-snapshot"`, `"usage"`}

// line mirrors the subset of an OpenClaw session line xusage consumes.
// Message and Data stay raw so a non-object value degrades to absent instead
// of failing the whole record, mirroring upstream's lenient deserializers.
type line struct {
	Type       *string         `json:"type"`
	CustomType *string         `json:"customType"`
	Data       json.RawMessage `json:"data"`
	ModelID    *string         `json:"modelId"`
	Model      *string         `json:"model"`
	Provider   *string         `json:"provider"`
	Message    json.RawMessage `json:"message"`
	Timestamp  json.RawMessage `json:"timestamp"`
}

// modelSource carries model/provider fields, either at the root of a
// model_change record or nested under its data key.
type modelSource struct {
	ModelID  *string `json:"modelId"`
	Model    *string `json:"model"`
	Provider *string `json:"provider"`
}

// message is the assistant message payload carrying usage and metadata.
type message struct {
	Role      *string         `json:"role"`
	Usage     *usage          `json:"usage"`
	Timestamp json.RawMessage `json:"timestamp"`
	ModelID   *string         `json:"modelId"`
	Model     *string         `json:"model"`
	Provider  *string         `json:"provider"`
}

// usage is the token usage block of an assistant message.
type usage struct {
	Input       lenientU64 `json:"input"`
	Output      lenientU64 `json:"output"`
	CacheRead   lenientU64 `json:"cacheRead"`
	CacheWrite  lenientU64 `json:"cacheWrite"`
	TotalTokens lenientU64 `json:"totalTokens"`
	Cost        *costBlock `json:"cost"`
}

// costBlock is the precomputed cost carried alongside usage.
type costBlock struct {
	Total *lenientF64 `json:"total"`
}

// lenientU64 accepts a JSON number or a numeric string; anything else is 0.
type lenientU64 uint64

func (u *lenientU64) UnmarshalJSON(b []byte) error {
	*u = 0
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		*u = lenientU64(n)
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		*u = lenientU64(f)
	}
	return nil
}

// lenientF64 accepts a JSON number or a numeric string; anything else is nil.
type lenientF64 float64

func (f *lenientF64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return errNotAValue
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return errNotAValue
	}
	*f = lenientF64(v)
	return nil
}

// errNotAValue marks input the lenient decoders treat as absent.
var errNotAValue = jsonInvalidValue{}

type jsonInvalidValue struct{}

func (jsonInvalidValue) Error() string { return "not a usable JSON value" }

// nonEmpty dereferences p, mapping empty strings to "" like upstream's
// non_empty_string deserializer (empty means absent).
func nonEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// parseSessionFile parses one session file's content into entries, tracking
// the current model/provider across model_change records.
func parseSessionFile(path string, content []byte) []model.Entry {
	sessionID := extractSessionID(path)
	fallback := fileModTime(path)

	var currentModel, currentProvider string
	var entries []model.Entry
	for _, raw := range bytes.Split(content, []byte{'\n'}) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || !admitsLine(raw) {
			continue
		}
		var rec line
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue // malformed lines are skipped, never fatal
		}
		if isModelChange(&rec) {
			source := rec.modelSource()
			if m := firstNonEmpty(source.ModelID, source.Model); m != "" {
				currentModel = m
			}
			if p := nonEmpty(source.Provider); p != "" {
				currentProvider = p
			}
			continue
		}
		e, ok := parseMessageEntry(&rec, sessionID, currentModel, currentProvider, fallback)
		if ok {
			entries = append(entries, e)
		}
	}
	return entries
}

// admitsLine reports whether the raw line contains any prefilter substring.
func admitsLine(line []byte) bool {
	for _, s := range prefilterStrings {
		if bytes.Contains(line, []byte(s)) {
			return true
		}
	}
	return false
}

// isModelChange reports whether the record updates tracked model/provider.
func isModelChange(rec *line) bool {
	if rec.Type != nil {
		if *rec.Type == "model_change" {
			return true
		}
		if *rec.Type == "custom" && nonEmpty(rec.CustomType) == "model-snapshot" {
			return true
		}
	}
	return false
}

// modelSource returns the record's model/provider source: the data object when
// it is one, otherwise the root-level fields.
func (rec *line) modelSource() modelSource {
	var ms modelSource
	if len(rec.Data) > 0 {
		if err := json.Unmarshal(rec.Data, &ms); err == nil && (ms.ModelID != nil || ms.Model != nil || ms.Provider != nil) {
			return ms
		}
	}
	return modelSource{ModelID: rec.ModelID, Model: rec.Model, Provider: rec.Provider}
}

// parseMessageEntry converts one message record into an entry. ok is false for
// records that carry no usable usage.
func parseMessageEntry(rec *line, sessionID, currentModel, currentProvider string, fallback time.Time) (model.Entry, bool) {
	if rec.Type == nil || *rec.Type != "message" {
		return model.Entry{}, false
	}
	var msg message
	if len(rec.Message) == 0 || json.Unmarshal(rec.Message, &msg) != nil {
		return model.Entry{}, false // non-object message degrades to absent
	}
	if nonEmpty(msg.Role) != "assistant" || msg.Usage == nil {
		return model.Entry{}, false
	}
	u := msg.Usage
	input, output := uint64(u.Input), uint64(u.Output)
	cacheRead, cacheWrite := uint64(u.CacheRead), uint64(u.CacheWrite)
	total := uint64(u.TotalTokens)

	// apply_total_token_fallback: fill unknown output first, then park any
	// remaining unexplained tokens in the extra bucket.
	extra := uint64(0)
	if known := input + output + cacheWrite + cacheRead; total > known {
		missing := total - known
		if output == 0 {
			output = missing
		} else {
			extra += missing
		}
	}
	if input+output+cacheWrite+cacheRead+extra == 0 {
		return model.Entry{}, false
	}
	if sum := input + output + cacheWrite + cacheRead + extra; total < sum {
		total = sum
	}
	extra = total - (input + output + cacheWrite + cacheRead)

	ts := fallback
	if t, ok := timestampFromValue(msg.Timestamp); ok {
		ts = t
	} else if t, ok := timestampFromValue(rec.Timestamp); ok {
		ts = t
	}
	modelName := firstNonEmpty(msg.ModelID, msg.Model)
	if modelName == "" {
		modelName = firstNonEmptyStr(currentModel)
	}
	if modelName == "" {
		modelName = "unknown"
	}
	provider := firstNonEmpty(msg.Provider)
	if provider == "" {
		provider = firstNonEmptyStr(currentProvider)
	}

	e := model.Entry{
		// Upstream synthesizes a composite id from the identity tuple; the
		// engine's loader dedup keys on it.
		ID:                  entryID(sessionID, ts, modelName, input, output, cacheWrite, cacheRead, extra, costValue(u.Cost)),
		Timestamp:           ts,
		Model:               "[openclaw] " + modelName,
		RawModel:            modelName,
		Project:             "openclaw",
		SessionID:           sessionID,
		Version:             provider,
		InputTokens:         input,
		OutputTokens:        output,
		CacheCreationTokens: cacheWrite,
		CacheReadTokens:     cacheRead,
		ExtraTotalTokens:    extra,
		Messages:            1,
	}
	if c := costValue(u.Cost); c != nil {
		v := *c
		e.ReportedCost = &v
	}
	return e, true
}

// costValue extracts cost.total when present and non-nil.
func costValue(c *costBlock) *float64 {
	if c == nil || c.Total == nil {
		return nil
	}
	v := float64(*c.Total)
	return &v
}

// timestampFromValue decodes a record timestamp: epoch milliseconds (JSON
// number) or an RFC3339 string. ok is false for any other shape.
func timestampFromValue(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 {
		return time.Time{}, false
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		if ms < 0 || ms > math.MaxInt64/10 {
			return time.Time{}, false
		}
		return time.UnixMilli(ms).UTC(), true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s)); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// fileModTime is the file's modification time, or the Unix epoch when unknown
// (upstream's fallback timestamp).
func fileModTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UTC()
	}
	return time.Unix(0, 0).UTC()
}

// entryID mirrors upstream's composite identity for dedup: two records with
// the same session, timestamp, model, token split, and cost are replays.
func entryID(sessionID string, ts time.Time, modelName string, input, output, cacheWrite, cacheRead, extra uint64, cost *float64) string {
	var b strings.Builder
	b.WriteString("openclaw:")
	b.WriteString(sessionID)
	b.WriteByte(':')
	b.WriteString(strconv.FormatInt(ts.UnixMilli(), 10))
	b.WriteByte(':')
	b.WriteString(modelName)
	for _, n := range []uint64{input, output, cacheWrite, cacheRead, extra} {
		b.WriteByte(':')
		b.WriteString(strconv.FormatUint(n, 10))
	}
	b.WriteByte(':')
	if cost != nil {
		b.WriteString(strconv.FormatFloat(*cost, 'g', -1, 64))
	}
	return b.String()
}

func firstNonEmpty(vals ...*string) string {
	for _, v := range vals {
		if s := nonEmpty(v); s != "" {
			return s
		}
	}
	return ""
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
