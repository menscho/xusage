package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"xusage/internal/model"
)

// message mirrors one OpenCode storage message document. Only the fields
// xusage consumes are declared; unknown keys are ignored.
type message struct {
	ID         string        `json:"id"`
	SessionID  string        `json:"sessionID"`
	ProviderID string        `json:"providerID"`
	ModelID    string        `json:"modelID"`
	Tokens     *tokens       `json:"tokens"`
	Time       *timeBlock    `json:"time"`
	Cost       *lenientFloat `json:"cost"`
}

// tokens is the per-message token usage block.
type tokens struct {
	Input  *lenientNumber `json:"input"`
	Output *lenientNumber `json:"output"`
	Cache  *cacheTokens   `json:"cache"`
	Total  *lenientNumber `json:"total"`
}

// cacheTokens carries read/write counts nested under the token usage block.
// A non-object value (number, string, array) decodes as absent instead of
// failing the record, mirroring upstream's lenient_object handling.
type cacheTokens struct {
	Read  *lenientNumber `json:"read"`
	Write *lenientNumber `json:"write"`
}

func (c *cacheTokens) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil // non-object cache: treated as absent, record survives
	}
	if raw, ok := fields["read"]; ok {
		_ = json.Unmarshal(raw, &c.Read)
	}
	if raw, ok := fields["write"]; ok {
		_ = json.Unmarshal(raw, &c.Write)
	}
	return nil
}

// timeBlock carries creation/completion timestamps as Unix milliseconds. Like
// cacheTokens it tolerates non-object values.
type timeBlock struct {
	Created *lenientNumber `json:"created"`
}

func (t *timeBlock) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil // non-object time block: treated as absent
	}
	if raw, ok := fields["created"]; ok {
		_ = json.Unmarshal(raw, &t.Created)
	}
	return nil
}

// lenientFloat accepts numbers and numeric strings, mirroring upstream's
// lenient handling of the stored cost value.
type lenientFloat struct {
	value float64
	ok    bool
}

// UnmarshalJSON accepts JSON numbers and numeric strings; null, empty, or
// non-numeric values leave the field unset.
func (f *lenientFloat) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(v, 0) {
		f.value = v
		f.ok = true
	}
	return nil
}

// lenientNumber accepts numbers, numeric strings, and null, mirroring the
// lenient parsing upstream applies to OpenCode payloads.
type lenientNumber struct {
	value uint64
	ok    bool
}

// UnmarshalJSON accepts JSON numbers (integral or floating), numeric strings,
// and null. Non-finite, negative, or non-numeric values leave the field unset.
func (n *lenientNumber) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 && f <= math.MaxUint64 && !isFractional(f) {
		n.value = uint64(f)
		n.ok = true
	}
	return nil
}

// isFractional reports whether f carries a fractional part.
func isFractional(f float64) bool {
	return math.Trunc(f) != f
}

// u64 returns the parsed value and whether a usable number was present.
func (n *lenientNumber) u64() (uint64, bool) {
	if n == nil {
		return 0, false
	}
	return n.value, n.ok
}

// float returns the parsed value and whether a usable number was present.
func (f *lenientFloat) float() (float64, bool) {
	if f == nil {
		return 0, false
	}
	return f.value, f.ok
}

// usage is the flattened token picture for one message after lenient parsing.
type usage struct {
	input, output, cacheRead, cacheWrite, total uint64
}

// parseMessage decodes one message document and maps it onto a model.Entry.
// It returns nil for records without usable usage: missing token block, empty
// model or provider, or an all-zero token picture.
func parseMessage(data []byte) *model.Entry {
	var msg message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil
	}
	return entryFromMessage(&msg)
}

// entryFromMessage maps a decoded message onto an Entry, mirroring upstream
// field semantics: cache.write -> cache creation, cache.read -> cache read,
// and the reported total only fills buckets the message left unknown.
func entryFromMessage(msg *message) *model.Entry {
	if msg.Tokens == nil {
		return nil
	}
	modelID := strings.TrimSpace(msg.ModelID)
	if modelID == "" {
		return nil
	}
	providerID := strings.TrimSpace(msg.ProviderID)
	if providerID == "" {
		return nil
	}

	u := usage{}
	if v, ok := msg.Tokens.Input.u64(); ok {
		u.input = v
	}
	if v, ok := msg.Tokens.Output.u64(); ok {
		u.output = v
	}
	if v, ok := msg.Tokens.Total.u64(); ok {
		u.total = v
	}
	if msg.Tokens.Cache != nil {
		if v, ok := msg.Tokens.Cache.Read.u64(); ok {
			u.cacheRead = v
		}
		if v, ok := msg.Tokens.Cache.Write.u64(); ok {
			u.cacheWrite = v
		}
	}

	// Upstream apply_total_token_fallback: a reported total larger than the
	// known buckets tops up output when it is the missing bucket, otherwise
	// the remainder lands in ExtraTotalTokens.
	known := u.input + u.output + u.cacheRead + u.cacheWrite
	extra := uint64(0)
	if u.total > known {
		missing := u.total - known
		if u.output == 0 {
			u.output = missing
		} else {
			extra = missing
		}
	}

	if u.input == 0 && u.output == 0 && u.cacheRead == 0 && u.cacheWrite == 0 && extra == 0 {
		return nil
	}

	ts := time.Time{}
	if msg.Time != nil {
		if v, ok := msg.Time.Created.u64(); ok && v > 0 {
			ts = time.UnixMilli(int64(v)).UTC()
		}
	}

	e := &model.Entry{
		ID:                  msg.ID,
		SessionID:           sessionIDOf(msg),
		Timestamp:           ts,
		Model:               modelID,
		RawModel:            modelID,
		Project:             projectLabel,
		InputTokens:         u.input,
		OutputTokens:        u.output,
		CacheCreationTokens: u.cacheWrite,
		CacheReadTokens:     u.cacheRead,
		ExtraTotalTokens:    extra,
	}
	if c, ok := msg.Cost.float(); ok {
		cost := c
		e.ReportedCost = &cost
	}
	return e
}

// sessionIDOf returns the message's sessionID, falling back to "unknown" when
// absent or empty (upstream loaded_session_id semantics).
func sessionIDOf(msg *message) string {
	if s := strings.TrimSpace(msg.SessionID); s != "" {
		return s
	}
	return "unknown"
}

// parseFile reads one OpenCode message file. Files hold a single pretty-printed
// JSON document; a JSONL file with one object per line parses identically.
func parseFile(path string) ([]model.Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if ents := parseMessageEntries(data); len(ents) > 0 {
		return ents, nil
	}
	return nil, nil
}

// parseMessageEntries tries a whole-file JSON document first, then falls back
// to per-line JSONL scanning so either storage shape parses. Malformed lines
// are skipped.
func parseMessageEntries(data []byte) []model.Entry {
	if e := parseMessage(data); e != nil {
		return []model.Entry{*e}
	}
	var entries []model.Entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if e := parseMessage(line); e != nil {
			entries = append(entries, *e)
		}
	}
	return entries
}
