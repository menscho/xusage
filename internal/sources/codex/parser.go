package codex

import (
	"encoding/json"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/adapter"
)

// usageInfo mirrors payload.info of a token_count event.
type usageInfo struct {
	Model     *string    `json:"model"`
	ModelName *string    `json:"model_name"`
	Last      *rawTokens `json:"last_token_usage"`
	Total     *rawTokens `json:"total_token_usage"`
}

// rawTokens mirrors the Codex token accounting objects. Spellings follow
// upstream types.rs: input_tokens/prompt_tokens/input, output_tokens/
// completion_tokens/output, cached_input_tokens/cache_read_input_tokens/
// cached_tokens, reasoning_output_tokens/reasoning_tokens.
type rawTokens struct {
	InputTokens        uint64 `json:"input_tokens"`
	CachedInputTokens  uint64 `json:"cached_input_tokens"`
	OutputTokens       uint64 `json:"output_tokens"`
	ReasoningOutTokens uint64 `json:"reasoning_output_tokens"`
	TotalTokens        uint64 `json:"total_tokens"`
	// Alternate spellings (headless exec logs and older clients).
	PromptTokens  uint64 `json:"prompt_tokens"`
	InputAlt      uint64 `json:"input"`
	CompletionTok uint64 `json:"completion_tokens"`
	OutputAlt     uint64 `json:"output"`
	ReasoningAlt  uint64 `json:"reasoning_tokens"`
	CacheReadAlt  uint64 `json:"cache_read_input_tokens"`
	CachedTokens  uint64 `json:"cached_tokens"`
}

// normalize folds the alternate field spellings into canonical counts,
// mirroring upstream's CodexRawUsage Deserialize impl: first present spelling
// wins per bucket, and an absent or zero recorded total is derived as
// input+output (reasoning is a subset of output and must not be added on top).
func (r rawTokens) normalize() rawTokens {
	in := r.InputTokens
	if in == 0 {
		in = r.PromptTokens
	}
	if in == 0 {
		in = r.InputAlt
	}
	out := r.OutputTokens
	if out == 0 {
		out = r.CompletionTok
	}
	if out == 0 {
		out = r.OutputAlt
	}
	reasoning := r.ReasoningOutTokens
	if reasoning == 0 {
		reasoning = r.ReasoningAlt
	}
	cached := r.CachedInputTokens
	if cached == 0 {
		cached = r.CacheReadAlt
	}
	if cached == 0 {
		cached = r.CachedTokens
	}
	total := r.TotalTokens
	if total == 0 {
		total = saturatingAdd(in, out)
	}
	return rawTokens{
		InputTokens:        in,
		CachedInputTokens:  cached,
		OutputTokens:       out,
		ReasoningOutTokens: reasoning,
		TotalTokens:        total,
	}
}

func saturatingAdd(a, b uint64) uint64 {
	sum := a + b
	if sum < a {
		return ^uint64(0)
	}
	return sum
}

// input returns the canonical input count (uncached portion is derived later).
func (r *rawTokens) input() uint64 { return r.InputTokens }

// cached returns the cached-input count.
func (r *rawTokens) cached() uint64 { return r.CachedInputTokens }

// output returns the canonical output count.
func (r *rawTokens) output() uint64 { return r.OutputTokens }

// payload mirrors the event payload envelope (token_count, turn_context,
// session_meta, thread_settings_applied).
type payload struct {
	PayloadType    string     `json:"type"`
	Model          *string    `json:"model"`
	ModelName      *string    `json:"model_name"`
	Info           *usageInfo `json:"info"`
	CWD            *string    `json:"cwd"`
	ThreadSettings *struct {
		ServiceTier *string `json:"service_tier"`
	} `json:"thread_settings"`
}

// rolloutLine mirrors one rollout record.
type rolloutLine struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   *payload        `json:"payload"`
}

// serviceTier maps recorded Codex tiers onto the fast flag, mirroring
// upstream codex_service_tier: "default"/"standard" are standard (non-fast)
// pricing, "fast"/"priority" is fast. An unrecognized tier returns false so
// the caller can clear a stale value; an absent key says nothing and keeps it.
func serviceTier(s *string) (fast, recognized bool) {
	if s == nil {
		return false, false
	}
	switch *s {
	case "default", "standard":
		return false, true
	case "fast", "priority":
		return true, true
	default:
		return false, false
	}
}

// parseTimestamp accepts RFC3339 strings and epoch millis/seconds numbers.
func parseTimestamp(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 {
		return time.Time{}, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
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
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
		if n > 10_000_000_000 { // already millis
			return time.UnixMilli(n).UTC(), true
		}
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

// modelFrom returns the first non-empty model spelling.
func modelFrom(ps ...*string) string {
	for _, p := range ps {
		if p != nil && strings.TrimSpace(*p) != "" {
			return strings.TrimSpace(*p)
		}
	}
	return ""
}

// parser carries per-file state across rollout lines.
type parser struct {
	sessionID string
	project   string
	model     string     // last seen turn_context / token_count model
	prevTotal *rawTokens // last cumulative total_token_usage snapshot
	fast      bool       // current service tier maps to fast pricing
}

// nextEntry builds an Entry from per-event token buckets. The caller resolves
// deltas against the running total first, so these values are final. The model
// name is interned so the (frequently repeated) Model/RawModel strings share
// one backing array per distinct value across the whole transcript.
func (p *parser) nextEntry(ts time.Time, modelName string, fast bool, in, cacheRead, out, extraTotal uint64) model.Entry {
	if modelName == "" {
		modelName = p.model
	}
	if modelName == "" {
		modelName = "gpt-5" // upstream's fallback model
	}
	p.model = modelName
	p.fast = fast
	m := adapter.Intern(modelName)
	return model.Entry{
		Timestamp:        ts,
		Model:            m,
		RawModel:         m,
		Project:          p.project,
		SessionID:        p.sessionID,
		InputTokens:      in,
		CacheReadTokens:  cacheRead,
		OutputTokens:     out,
		ExtraTotalTokens: extraTotal,
		Messages:         1,
		SpeedFast:        fast,
	}
}

// buckets splits one normalized token snapshot into display buckets: input
// (uncached), cache-read input, and output with reasoning folded in by
// summation — Codex reports reasoning as a subset of output.
func buckets(r *rawTokens) (in, cacheRead, out uint64) {
	cacheRead = minU64(r.cached(), r.input())
	in = subU64(r.input(), cacheRead)
	out = saturatingAdd(r.output(), r.ReasoningOutTokens)
	return in, cacheRead, out
}

// subU64 subtracts with saturation at zero.
func subU64(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return 0
}

// minU64 returns the smaller of two counts.
func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// diff computes current minus prev field-wise with saturation at zero,
// operating on normalized snapshots so alternate spellings compare correctly.
func diff(cur, prev *rawTokens) rawTokens {
	if prev == nil {
		return *cur
	}
	return rawTokens{
		InputTokens:        subU64(cur.input(), prev.input()),
		CachedInputTokens:  subU64(cur.cached(), prev.cached()),
		OutputTokens:       subU64(cur.output(), prev.output()),
		ReasoningOutTokens: subU64(cur.ReasoningOutTokens, prev.ReasoningOutTokens),
		TotalTokens:        subU64(cur.TotalTokens, prev.TotalTokens),
	}
}

// same reports whether two normalized token snapshots are identical (a
// repeated cumulative total means no new usage happened between the events).
func same(a, b *rawTokens) bool {
	if a == nil || b == nil {
		return false
	}
	return a.InputTokens == b.InputTokens &&
		a.CachedInputTokens == b.CachedInputTokens &&
		a.OutputTokens == b.OutputTokens &&
		a.ReasoningOutTokens == b.ReasoningOutTokens &&
		a.TotalTokens == b.TotalTokens
}
