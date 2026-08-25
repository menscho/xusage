package claude

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/adapter"
)

// usageBlock mirrors message.usage. Token fields are optPtr so an explicit
// null (rejected by hasUnsupportedNullField) stays distinguishable from a
// field that is simply absent (treated as zero, like upstream serde defaults).
type usageBlock struct {
	InputTokens         uint64  `json:"input_tokens"`
	OutputTokens        uint64  `json:"output_tokens"`
	CacheCreationTokens *uint64 `json:"cache_creation_input_tokens"`
	CacheReadTokens     optPtr  `json:"cache_read_input_tokens"`
	Speed               *string `json:"speed"`
	CacheCreation       *struct {
		Ephemeral5m uint64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h uint64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	// Iterations carries advisor sub-message usages (upstream ports each
	// "advisor_message" iteration as its own entry keyed to the advisor model).
	Iterations []usageIteration `json:"iterations"`
}

// usageIteration mirrors one message.usage.iterations[] record.
type usageIteration struct {
	Type         string  `json:"type"`
	Model        *string `json:"model"`
	InputTokens  uint64  `json:"input_tokens"`
	OutputTokens uint64  `json:"output_tokens"`
	CacheCreate  *uint64 `json:"cache_creation_input_tokens"`
	CacheRead    *uint64 `json:"cache_read_input_tokens"`
}

// optPtr is a nullable uint64: nil means the key was absent or null. It
// implements its own UnmarshalJSON because a custom struct type would reject
// plain JSON numbers.
type optPtr struct{ p *uint64 }

// UnmarshalJSON decodes a JSON number, or records null/absence as nil.
func (o *optPtr) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		o.p = nil
		return nil
	}
	var v uint64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.p = &v
	return nil
}

// Value returns the pointed-to count, or 0 when absent.
func (o optPtr) Value() uint64 {
	if o.p == nil {
		return 0
	}
	return *o.p
}

// messageBlock mirrors the message envelope.
type messageBlock struct {
	ID    *string     `json:"id"`
	Model *string     `json:"model"`
	Usage *usageBlock `json:"usage"`
}

// rawLine mirrors the subset of a Claude transcript line xusage consumes.
type rawLine struct {
	Timestamp         string        `json:"timestamp"`
	Version           *string       `json:"version"`
	SessionID         *string       `json:"sessionId"`
	RequestID         *string       `json:"requestId"`
	CostUSD           *float64      `json:"costUSD"`
	IsSidechain       *bool         `json:"isSidechain"`
	IsAPIErrorMessage *bool         `json:"isApiErrorMessage"`
	CWD               *string       `json:"cwd"`
	Message           *messageBlock `json:"message"`
}

// nullFields are the fields upstream rejects when explicitly JSON-null.
var nullFields = map[string]bool{
	"id": true, "cwd": true, "model": true, "speed": true, "costUSD": true,
	"version": true, "sessionId": true, "requestId": true,
	"isApiErrorMessage": true, "cache_read_input_tokens": true,
	"cache_creation_input_tokens": true,
}

// hasUnsupportedNullField reports whether any rejected field is set to null
// anywhere in the raw line. It scans for ":null" and inspects the key that
// immediately precedes it, mirroring upstream has_unsupported_null_field.
func hasUnsupportedNullField(line string) bool {
	return hasUnsupportedNullFieldBytes([]byte(line))
}

// hasUnsupportedNullFieldBytes does the scan on a byte slice so the parser can
// pass the scanner's reused buffer and avoid a per-line string→bytes
// allocation. It keeps the same textual ":null" + preceding-key logic as the
// string version; the string entry point (called by tests) wraps it.
func hasUnsupportedNullFieldBytes(line []byte) bool {
	needle := []byte(":null")
	for i := 0; i+5 <= len(line); {
		j := bytes.Index(line[i:], needle)
		if j < 0 {
			return false
		}
		nullIdx := i + j
		end := nullIdx - 1
		if end < 0 || line[end] != '"' {
			for end > 0 && line[end] != '"' {
				end--
			}
		}
		if end >= 0 && line[end] == '"' && end > 0 {
			start := end - 1
			for start > 0 && line[start] != '"' {
				start--
			}
			if line[start] == '"' && nullFields[string(line[start+1:end])] {
				return true
			}
		}
		i = nullIdx + 5
	}
	return false
}

// isSemverPrefix reports whether v starts with N.N.N (digits at each slot).
func isSemverPrefix(v string) bool {
	digits := func(s string) int {
		n := 0
		for n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		return n
	}
	a := digits(v)
	if a == 0 || a >= len(v) || v[a] != '.' {
		return false
	}
	b := digits(v[a+1:])
	if b == 0 || a+1+b >= len(v) || v[a+1+b] != '.' {
		return false
	}
	return digits(v[a+b+2:]) > 0
}

// parseTimestamp accepts RFC3339 timestamps with fractional seconds.
func parseTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseLine converts one transcript line into zero or more entries. The ok
// flag is false when the line carries no usable usage record. relPath is the
// transcript's own path (used for session/project extraction); passing it
// explicitly keeps parsing safe under the engine's parallel worker pool.
func parseLine(line, relPath string) ([]model.Entry, bool) {
	return parseLineBytes([]byte(line), relPath)
}

// parseLineBytes does the real work on a byte slice, so the engine's parallel
// parse workers can pass the scanner's reused buffer (sc.Bytes()) and skip a
// per-line string→bytes allocation. The string parseLine wrapper preserves the
// call signature for tests.
func parseLineBytes(line []byte, relPath string) ([]model.Entry, bool) {
	if !bytes.Contains(line, []byte(`"usage":{`)) { // fast prefilter
		return nil, false
	}
	if hasUnsupportedNullFieldBytes(line) {
		return nil, false
	}
	var rl rawLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return nil, false
	}
	ts, ok := parseTimestamp(rl.Timestamp)
	if !ok {
		return nil, false
	}
	if rl.Version != nil && !isSemverPrefix(*rl.Version) {
		return nil, false
	}
	if rl.Message == nil {
		return nil, false
	}
	if empty(rl.SessionID) || empty(rl.RequestID) || empty(rl.Message.ID) || empty(rl.Message.Model) {
		return nil, false
	}
	u := rl.Message.Usage
	if u == nil {
		return nil, false
	}
	modelName := *rl.Message.Model
	if modelName == "<synthetic>" {
		return nil, false
	}
	fast := u.Speed != nil && *u.Speed == "fast"
	if fast {
		modelName += "-fast"
	}
	modelName = adapter.Intern(modelName)
	rawModel := adapter.Intern(*rl.Message.Model)
	cc5m := uint64(0)
	if u.CacheCreationTokens != nil {
		cc5m = *u.CacheCreationTokens
	}
	cc1h := uint64(0)
	if u.CacheCreation != nil {
		cc5m = u.CacheCreation.Ephemeral5m
		cc1h = u.CacheCreation.Ephemeral1h
	}
	sessionID, _ := extractSessionParts(relPath)
	e := model.Entry{
		ID: *rl.Message.ID,
		// RequestID repeats across the main entry and each advisor sub-entry
		// copied from it, so intern it to share one backing array per turn.
		RequestID:           adapter.Intern(deref(rl.RequestID)),
		Timestamp:           ts,
		Model:               modelName,
		RawModel:            rawModel,
		SessionID:           sessionID,
		Version:             deref(rl.Version),
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheCreationTokens: cc5m,
		CacheCreation1h:     cc1h,
		CacheReadTokens:     u.CacheReadTokens.Value(),
		ReportedCost:        rl.CostUSD,
		SpeedFast:           fast,
		Messages:            1,
		IsSidechain:         rl.IsSidechain != nil && *rl.IsSidechain,
		IsAPIError:          rl.IsAPIErrorMessage != nil && *rl.IsAPIErrorMessage,
		UsageLimitReset:     usageLimitReset(line, rl.IsAPIErrorMessage),
	}
	// Pre-size to the worst case (one entry per iteration) so the advisor loop
	// never reallocates the per-line slice.
	entries := make([]model.Entry, 0, 1+len(u.Iterations))
	entries = append(entries, e)
	// Advisor iterations become their own entries under the advisor model with
	// no reported cost, mirroring upstream advisor_usages_from_line. The id
	// gets an ":advisor:<n>" suffix so dedup keeps them distinct from the
	// parent message.
	for i := range u.Iterations {
		it := &u.Iterations[i]
		if it.Type != "advisor_message" || it.Model == nil || *it.Model == "" {
			continue
		}
		cc5 := uint64(0)
		if it.CacheCreate != nil {
			cc5 = *it.CacheCreate
		}
		cr := uint64(0)
		if it.CacheRead != nil {
			cr = *it.CacheRead
		}
		a := e
		a.ID = e.ID + ":advisor:" + strconv.Itoa(i)
		am := adapter.Intern(*it.Model)
		a.Model = am
		a.RawModel = am
		a.InputTokens = it.InputTokens
		a.OutputTokens = it.OutputTokens
		a.CacheCreationTokens = cc5
		a.CacheCreation1h = 0
		a.CacheReadTokens = cr
		a.ReportedCost = nil
		a.SpeedFast = false
		entries = append(entries, a)
	}
	return entries, true
}

func empty(s *string) bool { return s != nil && *s == "" }
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// usageLimitReset extracts the epoch-seconds reset stamp from Claude's
// "Claude AI usage limit reached(...)|<unix>" API error text. It takes bytes so
// the hot path can pass the scanner's reused buffer directly.
func usageLimitReset(line []byte, isAPIError *bool) *time.Time {
	if isAPIError == nil || !*isAPIError {
		return nil
	}
	const marker = "Claude AI usage limit reached"
	i := bytes.Index(line, []byte(marker))
	if i < 0 {
		return nil
	}
	rest := line[i+len(marker):]
	pipe := bytes.IndexByte(rest, '|')
	if pipe < 0 {
		return nil
	}
	rest = rest[pipe+1:]
	rest = bytes.TrimLeft(rest, " \t")
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return nil
	}
	var sec int64
	for _, c := range rest[:end] {
		sec = sec*10 + int64(c-'0')
		if sec > 1<<62 {
			return nil
		}
	}
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}
