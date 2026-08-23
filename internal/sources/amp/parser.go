package amp

import (
	"encoding/json"
	"strconv"
	"strings"

	"xusage/internal/model"
	"xusage/internal/sources/jsonx"
)

// thread mirrors the Amp thread document: id, messages, optional ledger.
// Polymorphic leaves stay as raw JSON so an unexpected shape degrades to
// "absent" instead of failing the whole file, mirroring upstream's lenient
// navigation.
type thread struct {
	ID          string          `json:"id"`
	Messages    json.RawMessage `json:"messages"`
	UsageLedger json.RawMessage `json:"usageLedger"`
}

// ledgerEvent is one usage-ledger row. Cache tokens are NOT carried here;
// upstream joins them from the chat messages via to_message_id.
type ledgerEvent struct {
	ID          json.RawMessage `json:"id"` // number or string
	Timestamp   string          `json:"timestamp"`
	Model       string          `json:"model"`
	Tokens      json.RawMessage `json:"tokens"`
	ToMessageID json.RawMessage `json:"to_message_id"` // numeric message id
}

// chatMessage is one chat message carrying a usage block (legacy shape).
type chatMessage struct {
	Role      string          `json:"role"`
	Model     string          `json:"model"`
	MessageID json.RawMessage `json:"messageId"` // number or string
	Timestamp string          `json:"timestamp"`
	Usage     json.RawMessage `json:"usage"`
}

// usageBlock mirrors the per-message usage object.
type usageBlock struct {
	Timestamp  string `json:"timestamp"`
	Model      string `json:"model"`
	Input      uint64 `json:"inputTokens"`
	Output     uint64 `json:"outputTokens"`
	CacheRead  uint64 `json:"cacheReadInputTokens"`
	CacheWrite uint64 `json:"cacheCreationInputTokens"`
	Total      uint64 `json:"totalTokens"`
}

// tokenBlock mirrors the ledger's tokens object. Only input/output/total are
// read: upstream ignores any cache counts embedded in ledger tokens.
type tokenBlock struct {
	Input  uint64 `json:"input"`
	Output uint64 `json:"output"`
	Total  uint64 `json:"total"`
}

// parse decodes one thread file into entries. A missing id or unreadable
// shapes yield no entries rather than an error.
func parse(path string) ([]model.Entry, error) {
	raw, err := readFile(path)
	if err != nil {
		return nil, err
	}
	var t thread
	if json.Unmarshal(raw, &t) != nil || t.ID == "" {
		return nil, nil
	}
	// Messages decode leniently: malformed elements are dropped later rather
	// than failing the thread.
	var msgs []json.RawMessage
	_ = json.Unmarshal(t.Messages, &msgs)
	// The ledger wins only when its events field holds a real array (possibly
	// empty); a missing or non-array events value falls back to the messages.
	if events, ok := ledgerEvents(t.UsageLedger); ok {
		return parseLedger(t.ID, msgs, events), nil
	}
	return parseMessages(t.ID, msgs), nil
}

// ledgerEvents extracts the events array from a usageLedger object. ok is true
// only when events is present and is a JSON array; malformed elements are
// dropped by the caller rather than failing the thread.
func ledgerEvents(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var wrapper struct {
		Events json.RawMessage `json:"events"`
	}
	if json.Unmarshal(raw, &wrapper) != nil || len(wrapper.Events) == 0 {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(wrapper.Events, &items) != nil {
		return nil, false // non-array events: treated as absent
	}
	return items, true
}

// parseLedger maps usage-ledger events onto entries. Cache buckets come from
// the thread's assistant messages joined by to_message_id, exactly like
// upstream; the lone total (when the split is absent) lands in
// ExtraTotalTokens via the shared fallback.
func parseLedger(threadID string, msgs, items []json.RawMessage) []model.Entry {
	cache := cacheTokensByMessageID(msgs)

	var entries []model.Entry
	for _, item := range items {
		var ev ledgerEvent
		if json.Unmarshal(item, &ev) != nil {
			continue // malformed element: skip, keep the rest
		}
		ms, ok := jsonx.Time(ev.Timestamp)
		if !ok || ev.Model == "" {
			continue
		}
		var toks tokenBlock
		if len(ev.Tokens) == 0 || json.Unmarshal(ev.Tokens, &toks) != nil {
			continue
		}
		var cc, cr uint64
		if msgID, ok := i64Of(ev.ToMessageID); ok {
			if pair, found := cache[msgID]; found {
				cc, cr = pair[0], pair[1]
			}
		}
		in, out, extra := totalFallback(toks.Input, toks.Output, cc, cr, toks.Total)
		if in == 0 && out == 0 && cc == 0 && cr == 0 && extra == 0 {
			continue
		}
		entries = append(entries, model.Entry{
			ID:                  jsonString(ev.ID),
			Timestamp:           msTime(ms),
			Model:               ev.Model,
			RawModel:            ev.Model,
			Project:             projectFor(),
			SessionID:           threadID,
			InputTokens:         in,
			OutputTokens:        out,
			CacheCreationTokens: cc,
			CacheReadTokens:     cr,
			ExtraTotalTokens:    extra,
			Messages:            1,
		})
	}
	return entries
}

// parseMessages maps legacy assistant-message usage blocks onto entries.
func parseMessages(threadID string, items []json.RawMessage) []model.Entry {
	if len(items) == 0 {
		return nil
	}
	var entries []model.Entry
	for _, item := range items {
		var m chatMessage
		if json.Unmarshal(item, &m) != nil {
			continue // malformed element: skip, keep the rest
		}
		if m.Role != "assistant" {
			continue
		}
		var u usageBlock
		if len(m.Usage) == 0 || json.Unmarshal(m.Usage, &u) != nil {
			continue
		}
		ts := u.Timestamp
		if ts == "" {
			ts = m.Timestamp
		}
		modelName := u.Model
		if modelName == "" {
			modelName = m.Model
		}
		ms, ok := jsonx.Time(ts)
		if !ok || modelName == "" {
			continue
		}
		in, out, extra := totalFallback(u.Input, u.Output, u.CacheWrite, u.CacheRead, u.Total)
		if in == 0 && out == 0 && u.CacheWrite == 0 && u.CacheRead == 0 && extra == 0 {
			continue
		}
		entries = append(entries, model.Entry{
			ID:                  jsonString(m.MessageID),
			Timestamp:           msTime(ms),
			Model:               modelName,
			RawModel:            modelName,
			Project:             projectFor(),
			SessionID:           threadID,
			InputTokens:         in,
			OutputTokens:        out,
			CacheCreationTokens: u.CacheWrite,
			CacheReadTokens:     u.CacheRead,
			ExtraTotalTokens:    extra,
			Messages:            1,
		})
	}
	return entries
}

// cacheTokensByMessageID collects (cacheCreation, cacheRead) from assistant
// messages keyed by their numeric messageId — the join source for ledger
// events, mirroring upstream cache_tokens_by_message_id. Malformed message
// elements are skipped.
func cacheTokensByMessageID(msgs []json.RawMessage) map[int64][2]uint64 {
	cache := make(map[int64][2]uint64)
	for _, item := range msgs {
		var m chatMessage
		if json.Unmarshal(item, &m) != nil || m.Role != "assistant" {
			continue
		}
		id, ok := i64Of(m.MessageID)
		if !ok {
			continue
		}
		var u usageBlock
		if len(m.Usage) == 0 || json.Unmarshal(m.Usage, &u) != nil {
			continue
		}
		cache[id] = [2]uint64{u.CacheWrite, u.CacheRead}
	}
	return cache
}

// totalFallback applies the upstream apply_total_token_fallback ordering: a
// reported total larger than the named buckets first backfills zero output,
// then spills into ExtraTotalTokens.
func totalFallback(in, out, cc, cr, total uint64) (uint64, uint64, uint64) {
	known := in + out + cc + cr
	if total <= known {
		return in, out, 0
	}
	missing := total - known
	if out == 0 {
		return in, missing, 0
	}
	return in, out, missing
}

// jsonString renders a raw JSON value (string or number) as a Go string.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return itoa(n)
	}
	return ""
}

// i64Of renders a raw JSON number (or numeric string) as int64, mirroring
// upstream's Value::as_i64 navigation for polymorphic ids.
func i64Of(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
		return n, true
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int64(f), true
	}
	return 0, false
}
