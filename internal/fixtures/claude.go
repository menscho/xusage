package fixtures

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ClaudeLineOpts controls one synthetic Claude-shape JSONL line. Zero-value
// fields select deterministic defaults; Seed drives generated identifiers.
type ClaudeLineOpts struct {
	Timestamp time.Time // zero = 2025-06-01T12:00:00Z
	Version   string    // default "1.0.14"; set to omit the field entirely

	ID        string // message.id; default msg_<Seed>
	RequestID string // requestId; default req_<Seed>
	SessionID string // sessionId; default "sess-fixture"

	Model     string // message.model; default "claude-sonnet-4-20250514"
	Input     uint64
	Output    uint64
	Cache5m   uint64 // flat cache_creation_input_tokens, and 5m bucket when split
	Cache1h   uint64 // >0 switches to the structured cache_creation split object
	CacheRead uint64

	CostUSD        *float64 // nil omits costUSD entirely
	SpeedFast      bool     // usage.speed == "fast"
	SpeedStandard  bool     // usage.speed == "standard" (ignored by parsers)
	Sidechain      bool
	APIError       bool      // isApiErrorMessage:true
	LimitReset     time.Time // non-zero embeds "...usage limit reached|<unix>" in content
	NoUsage        bool      // emit an assistant line without a usage block
	Malformed      bool      // emit truncated invalid JSON (still newline-terminated)
	NullField      string    // top-level field rendered as explicit null
	NullUsageField string    // field inside message.usage rendered as explicit null
	Synthetic      bool      // message.model == "<synthetic>"
	BadVersion     string    // non-semver version value (overrides Version)
	Seed           int64
}

// ClaudeLine renders one Claude Code usage record as a JSONL line, mirroring
// the upstream log shape the claude adapter parses: top-level timestamp,
// version, sessionId, requestId, cwd, costUSD, isSidechain, isApiErrorMessage,
// plus a message object carrying id, model and a usage block.
func ClaudeLine(o ClaudeLineOpts) string {
	if o.Malformed {
		return `{"message":{"id":"msg_trunc","model":"claude-sonnet-4-20250514","usage":{"in` + "\n"
	}
	ts := o.Timestamp
	if ts.IsZero() {
		ts = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	}
	model := o.Model
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}
	if o.Synthetic {
		model = "<synthetic>"
	}

	var b strings.Builder
	b.Grow(512)
	b.WriteString(`{"parentUuid":"u1","isSidechain":`)
	b.WriteString(boolJSON(o.Sidechain))
	b.WriteString(`,"userType":"external","cwd":"/home/dev/project-alpha"`)
	b.WriteString(`,"sessionId":` + strJSON(optString(o.SessionID, "sess-fixture"), o.NullField == "sessionId"))
	b.WriteString(`,"version":`)
	if o.BadVersion != "" {
		b.WriteString(strJSON(o.BadVersion, false))
	} else if o.Version != "" {
		b.WriteString(strJSON(o.Version, o.NullField == "version"))
	} else {
		b.WriteString(`"1.0.14"`)
	}
	b.WriteString(`,"requestId":` + strJSON(optString(o.RequestID, "req_"+strconv.FormatInt(o.Seed, 10)), o.NullField == "requestId"))
	b.WriteString(`,"timestamp":` + strJSON(rfc3339Millis(ts), false))
	b.WriteString(`,"message":{`)
	if !o.NoUsage {
		b.WriteString(`"id":` + strJSON(optString(o.ID, "msg_"+strconv.FormatInt(o.Seed, 10)), o.NullField == "id") + `,`)
	}
	b.WriteString(`"model":` + strJSON(model, o.NullField == "model"))
	if !o.NoUsage {
		b.WriteString(`,"usage":{`)
		b.WriteString(`"input_tokens":` + numJSON(o.Input, o.NullUsageField == "input_tokens"))
		b.WriteString(`,"cache_creation_input_tokens":` + numJSON(o.Cache5m, o.NullUsageField == "cache_creation_input_tokens"))
		if o.Cache1h > 0 {
			b.WriteString(`,"cache_creation":{"ephemeral_5m_input_tokens":` + numJSON(o.Cache5m, false))
			b.WriteString(`,"ephemeral_1h_input_tokens":` + numJSON(o.Cache1h, false) + `}`)
		}
		b.WriteString(`,"output_tokens":` + numJSON(o.Output, false))
		b.WriteString(`,"cache_read_input_tokens":` + numJSON(o.CacheRead, o.NullUsageField == "cache_read_input_tokens"))
		switch {
		case o.SpeedFast:
			b.WriteString(`,"speed":"fast"`)
		case o.SpeedStandard:
			b.WriteString(`,"speed":"standard"`)
		}
		b.WriteString(`}`)
	}
	b.WriteString(`}`)
	b.WriteString(`,"content":` + contentJSON(o))
	b.WriteString(`,"isApiErrorMessage":` + boolJSON(o.APIError))
	if o.CostUSD != nil {
		b.WriteString(`,"costUSD":` + floatJSON(*o.CostUSD))
	} else if o.NullField == "costUSD" {
		b.WriteString(`,"costUSD":null`)
	}
	b.WriteString("}\n")
	return b.String()
}

// contentJSON renders the assistant content; API-error lines carry the
// "Claude AI usage limit reached|<epoch>" marker when LimitReset is set.
func contentJSON(o ClaudeLineOpts) string {
	if o.APIError && !o.LimitReset.IsZero() {
		return fmt.Sprintf(`[{{"type":"text","text":"Claude AI usage limit reached|%d"}}]`, o.LimitReset.Unix())
	}
	return `"ok"`
}

// rfc3339Millis renders t in UTC with millisecond precision, matching the
// millisecond timestamps real logs carry.
func rfc3339Millis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// strJSON quotes s, or renders null when isNull.
func strJSON(s string, isNull bool) string {
	if isNull {
		return "null"
	}
	return `"` + s + `"`
}

// numJSON renders n, or null when isNull.
func numJSON(n uint64, isNull bool) string {
	if isNull {
		return "null"
	}
	return strconv.FormatUint(n, 10)
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// floatJSON renders f via %g so costs stay short and deterministic.
func floatJSON(f float64) string { return fmt.Sprintf("%g", f) }

func optString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// MustCost returns a pointer to f (helper for ClaudeLineOpts.CostUSD).
func MustCost(f float64) *float64 { return &f }
