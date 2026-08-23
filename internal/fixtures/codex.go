package fixtures

import (
	"fmt"
	"strings"
	"time"
)

// CodexLineOpts controls one synthetic Codex rollout record. Zero-value
// fields select deterministic defaults.
type CodexLineOpts struct {
	Timestamp time.Time // zero = 2025-06-01T12:00:00Z
	LineType  string    // session_meta | turn_context | event_msg (default event_msg)

	// TokenCount convenience fields: when Payload is empty and LineType is
	// event_msg, a token_count payload is rendered from these.
	Input     uint64
	Cached    uint64
	Output    uint64
	Reasoning uint64
	Total     uint64 // defaults to Input+Cached+Output+Reasoning when zero
	TotalOnly bool   // emit total_token_usage carrying only total_tokens
	UseLast   bool   // wrap counts in last_token_usage instead of total_token_usage
	Model     string

	Payload string // pre-rendered payload JSON object body (overrides the above)
}

// CodexLine renders one Codex rollout JSONL record. LineType defaults to
// "event_msg"; when Payload is empty a token_count payload is synthesized
// from the token fields, mirroring the cumulative total_token_usage shape
// real rollouts carry.
func CodexLine(o CodexLineOpts) string {
	kind := o.LineType
	if kind == "" {
		kind = "event_msg"
	}
	ts := o.Timestamp
	if ts.IsZero() {
		ts = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	}
	timestamp := rfc3339Millis(ts)

	if kind == "session_meta" {
		return SessionMeta(ts, "", "")
	}
	if kind == "turn_context" {
		model := optString(o.Model, "gpt-5.2")
		return fmt.Sprintf(`{"timestamp":%s,"type":"turn_context","payload":{"model":%s}}`+"\n",
			strJSON(timestamp, false), strJSON(model, false))
	}

	var payload string
	if o.Payload != "" {
		payload = o.Payload
	} else {
		total := o.Total
		if total == 0 {
			total = o.Input + o.Cached + o.Output + o.Reasoning
		}
		var info string
		if o.TotalOnly {
			info = `"total_token_usage":{"total_tokens":` + fmt.Sprintf("%d", total) + `}`
		} else {
			usage := fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,`+
				`"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}`,
				o.Input, o.Cached, o.Output, o.Reasoning, total)
			field := `"total_token_usage":`
			if o.UseLast {
				field = `"last_token_usage":`
			}
			info = field + usage
			if o.Model != "" {
				info += `,"model":` + strJSON(o.Model, false)
			}
		}
		payload = `"type":"token_count","info":{` + info + `}`
	}
	return fmt.Sprintf(`{"timestamp":%s,"type":"event_msg","payload":{%s}}`+"\n",
		strJSON(timestamp, false), payload)
}

// SessionMeta renders a Codex session_meta record carrying the thread id and
// working directory; empty values omit the corresponding payload field.
func SessionMeta(ts time.Time, id, cwd string) string {
	if ts.IsZero() {
		ts = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"timestamp":%s,"type":"session_meta","payload":{`,
		strJSON(rfc3339Millis(ts), false))
	first := true
	if id != "" {
		b.WriteString(`"id":` + strJSON(id, false))
		first = false
	}
	if cwd != "" {
		if !first {
			b.WriteByte(',')
		}
		b.WriteString(`"cwd":` + strJSON(cwd, false))
	}
	b.WriteString(`}}` + "\n")
	return b.String()
}
