// Parsing of Codebuff chat-messages.json files into normalized usage entries.
//
// Port of rust/adapters/codebuff/src/parser.rs: each file is a JSON array of
// chat messages; assistant-shaped messages carry token usage either directly
// in metadata.usage / metadata.codebuff.usage or, as a fallback, in the last
// assistant entry of the runState message history.

package codebuff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xusage/internal/model"
)

// defaultModel is used when an assistant message carries usage but no model.
const defaultModel = "codebuff-unknown"

// chatContext carries the path-derived identity of one chat file.
type chatContext struct {
	chatID    string // immediate parent directory name (the chat id)
	sessionID string // "<channel>/<project>/<chatID>"
}

// deriveContext mirrors upstream derive_context: the session id is composed
// from the channel (two levels above the project directory), the project
// directory name, and the chat directory name; missing pieces fall back to
// "unknown" / "manicode".
func deriveContext(path string) chatContext {
	dir := filepath.Dir(path)
	chatID := filepath.Base(dir)
	chatsDir := filepath.Dir(dir)
	projectDir := filepath.Dir(chatsDir)
	project := filepath.Base(projectDir)
	// Channel is the data-root directory itself (two levels above the project
	// directory): ~/.config/manicode/projects/... -> "manicode".
	channel := filepath.Base(filepath.Dir(filepath.Dir(projectDir)))
	switch channel {
	case "", ".", "/", `\`:
		channel = "manicode"
	}
	switch project {
	case "", ".", "/", `\`:
		project = "unknown"
	}
	switch chatID {
	case "", ".", "/", `\`:
		chatID = "unknown"
	}
	return chatContext{
		chatID:    chatID,
		sessionID: channel + "/" + project + "/" + chatID,
	}
}

// assistantUsage accumulates token counts merged from an assistant message's
// several possible usage locations.
type assistantUsage struct {
	model         string
	credits       float64
	inputTokens   uint64
	outputTokens  uint64
	cacheCreation uint64
	cacheRead     uint64
	extraTotal    uint64
}

// ParseChatFile reads one chat-messages.json and returns its usage entries.
// A file that is not a JSON array of objects yields zero entries, never an
// error, matching upstream load_chat_file.
func ParseChatFile(path string) ([]model.Entry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(content, &messages); err != nil {
		return nil, nil // malformed file: no entries, not an error
	}
	ctx := deriveContext(path)
	chatTS := chatIDTimestamp(ctx.chatID)
	fileTS := fileModTime(path)

	var entries []model.Entry
	for ordinal, raw := range messages {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if !isAssistantMessage(msg) {
			continue
		}
		usage := extractAssistantUsage(msg)
		if !hasSignal(usage) {
			continue
		}
		ts := messageTimestamp(msg)
		if ts.IsZero() {
			ts = chatTS
		}
		if ts.IsZero() {
			ts = fileTS
		}
		mdl := usage.model
		if mdl == "" {
			mdl = defaultModel
		}
		entries = append(entries, model.Entry{
			ID:                  dedupKey(msg, ctx.sessionID, ts, mdl, usage, ordinal),
			Timestamp:           ts.UTC(),
			Model:               mdl,
			RawModel:            mdl,
			SessionID:           ctx.sessionID,
			InputTokens:         usage.inputTokens,
			OutputTokens:        usage.outputTokens,
			CacheCreationTokens: usage.cacheCreation,
			CacheReadTokens:     usage.cacheRead,
			ExtraTotalTokens:    usage.extraTotal,
			Messages:            1,
		})
	}
	return entries, nil
}

// isAssistantMessage reports whether the message looks like an assistant turn:
// variant or role is one of ai / agent / assistant.
func isAssistantMessage(msg map[string]json.RawMessage) bool {
	s := stringField(msg, "variant")
	if s == "" {
		s = stringField(msg, "role")
	}
	switch s {
	case "ai", "agent", "assistant":
		return true
	default:
		return false
	}
}

// extractAssistantUsage merges usage from metadata.usage, then
// metadata.codebuff.usage, then the runState message history fallback, then
// the message-level credits field.
func extractAssistantUsage(msg map[string]json.RawMessage) assistantUsage {
	var usage assistantUsage
	meta := objectField(msg, "metadata")
	if meta != nil {
		usage.model = stringField(meta, "model")
		mergeFallback(&usage, parseUsageObject(objectField(meta, "usage")))
		if codebuff := objectField(meta, "codebuff"); codebuff != nil {
			mergeFallback(&usage, parseUsageObject(objectField(codebuff, "usage")))
		}
		if rs := runStateUsage(meta); rs != nil {
			mergeFallback(&usage, *rs)
		}
	}
	if credits := numberField(msg, "credits"); credits > 0 && usage.credits <= 0 {
		usage.credits = credits
	}
	return usage
}

// runStateUsage walks metadata.runState.sessionState.mainAgentState.messageHistory
// in reverse, merging providerOptions usage from assistant entries. It returns
// nil when no assistant entry carries any signal.
func runStateUsage(meta map[string]json.RawMessage) *assistantUsage {
	runState := objectField(meta, "runState")
	sessionState := objectField(runState, "sessionState")
	mainState := objectField(sessionState, "mainAgentState")
	historyRaw, ok := mainState["messageHistory"]
	if !ok {
		return nil
	}
	var history []json.RawMessage
	if err := json.Unmarshal(historyRaw, &history); err != nil {
		return nil
	}
	var merged assistantUsage
	found := false
	for i := len(history) - 1; i >= 0; i-- {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(history[i], &item); err != nil {
			continue
		}
		if stringField(item, "role") != "assistant" {
			continue
		}
		opts := objectField(item, "providerOptions")
		if opts == nil {
			continue
		}
		var entryUsage assistantUsage
		mergeFallback(&entryUsage, parseUsageObject(objectField(opts, "usage")))
		if codebuff := objectField(opts, "codebuff"); codebuff != nil {
			mergeFallback(&entryUsage, parseUsageObject(objectField(codebuff, "usage")))
			if m := stringField(codebuff, "model"); m != "" && entryUsage.model == "" {
				entryUsage.model = m
			}
		}
		if hasSignal(entryUsage) || entryUsage.model != "" {
			found = true
		}
		mergeFallback(&merged, entryUsage)
	}
	if !found {
		return nil
	}
	return &merged
}

// parseUsageObject reads token counts from one usage record, accepting both
// camelCase and snake_case key spellings plus OpenAI-style prompt/completion
// token names. A lone total fills missing output tokens first (upstream
// apply_total_token_fallback), overflowing into extraTotal only when output
// is already known.
func parseUsageObject(record map[string]json.RawMessage) assistantUsage {
	var usage assistantUsage
	usage.inputTokens = pickU64(record, "inputTokens", "input_tokens", "promptTokens", "prompt_tokens")
	usage.outputTokens = pickU64(record, "outputTokens", "output_tokens", "completionTokens", "completion_tokens")
	usage.cacheRead = max64(
		pickU64(record, "cacheReadInputTokens", "cache_read_input_tokens"),
		pickNestedU64(record, "promptTokensDetails", "cachedTokens"),
		pickNestedU64(record, "prompt_tokens_details", "cached_tokens"),
	)
	usage.cacheCreation = pickU64(record,
		"cacheCreationInputTokens", "cache_creation_input_tokens",
		"cacheCreationTokens", "cache_creation_tokens",
		"cachedTokensCreated", "cached_tokens_created")

	total := pickU64(record, "totalTokens", "total_tokens", "total")
	known := usage.inputTokens + usage.outputTokens + usage.cacheCreation + usage.cacheRead + usage.extraTotal
	if missing := saturatingSub(total, known); missing > 0 {
		if usage.outputTokens == 0 {
			usage.outputTokens = missing
		} else {
			usage.extraTotal += missing
		}
	}

	usage.credits = numberField(record, "credits")
	usage.model = stringField(record, "model")
	return usage
}

// mergeFallback fills zero-valued fields of target from fallback, mirroring
// upstream merge_fallback.
func mergeFallback(target *assistantUsage, fallback assistantUsage) {
	if target.inputTokens == 0 {
		target.inputTokens = fallback.inputTokens
	}
	if target.outputTokens == 0 {
		target.outputTokens = fallback.outputTokens
	}
	if target.cacheCreation == 0 {
		target.cacheCreation = fallback.cacheCreation
	}
	if target.cacheRead == 0 {
		target.cacheRead = fallback.cacheRead
	}
	if target.extraTotal == 0 {
		target.extraTotal = fallback.extraTotal
	}
	if target.credits <= 0 {
		target.credits = fallback.credits
	}
	if target.model == "" {
		target.model = fallback.model
	}
}

// hasSignal reports whether any token bucket or credit count is positive.
func hasSignal(u assistantUsage) bool {
	return u.inputTokens > 0 || u.outputTokens > 0 ||
		u.cacheCreation > 0 || u.cacheRead > 0 || u.extraTotal > 0 || u.credits > 0
}

// messageTimestamp reads message.timestamp, then createdAt, then
// metadata.timestamp. Numeric values are unix millis (or seconds below the
// 10-digit threshold). Zero time means "not present".
func messageTimestamp(msg map[string]json.RawMessage) time.Time {
	if ts := timestampValue(msg["timestamp"]); !ts.IsZero() {
		return ts
	}
	if ts := timestampValue(msg["createdAt"]); !ts.IsZero() {
		return ts
	}
	if meta := objectField(msg, "metadata"); meta != nil {
		return timestampValue(meta["timestamp"])
	}
	return time.Time{}
}

// timestampValue parses a string (RFC 3339-ish) or numeric (millis) timestamp.
func timestampValue(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseTimestamp(s)
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		millis := n
		if n < 10_000_000_000 { // seconds-looking value
			millis = n * 1000
		}
		if millis > 0 {
			return time.UnixMilli(millis)
		}
	}
	return time.Time{}
}

// parseTimestamp accepts RFC 3339 with optional fractional seconds; trailing
// "Z" and offsets are handled by time.Parse layouts.
func parseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// chatIDTimestamp recovers a timestamp from a Codebuff chat directory name of
// the form "2026-01-02T03-04-05.000Z" (dashes instead of colons in the time).
func chatIDTimestamp(chatID string) time.Time {
	date, timePart, ok := strings.Cut(chatID, "T")
	if !ok {
		return time.Time{}
	}
	// Replace the first two dashes in the time part with colons.
	replaced := []rune(timePart)
	dashes := 0
	for i, r := range replaced {
		if r == '-' {
			replaced[i] = ':'
			dashes++
			if dashes == 2 {
				break
			}
		}
	}
	return parseTimestamp(date + "T" + string(replaced))
}

// fileModTime returns the file's modification time in UTC, or zero time.
func fileModTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime().UTC()
}

// dedupKey builds the upstream dedup identity: message id when present,
// otherwise a composite of session, timestamp, model, ordinal, and token
// counts. The engine keys its dedup on Provider+ID, so reusing this value as
// Entry.ID reproduces upstream last-wins behavior across files.
func dedupKey(msg map[string]json.RawMessage, sessionID string, ts time.Time, mdl string, u assistantUsage, ordinal int) string {
	if id := stringField(msg, "id"); id != "" {
		return "codebuff:" + sessionID + ":" + id
	}
	return fmt.Sprintf("codebuff:%s:%s:%s:%d:%d:%d:%d:%d:%d",
		sessionID, ts.UTC().Format("2006-01-02T15:04:05.000Z"), mdl, ordinal,
		u.inputTokens, u.outputTokens, u.cacheRead, u.cacheCreation, u.extraTotal)
}

// --- small JSON field helpers ------------------------------------------------

// stringField returns a trimmed non-empty string field, or "".
func stringField(msg map[string]json.RawMessage, key string) string {
	raw, ok := msg[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	s = strings.TrimSpace(s)
	return s
}

// objectField returns a nested JSON object, or nil.
func objectField(msg map[string]json.RawMessage, key string) map[string]json.RawMessage {
	raw, ok := msg[key]
	if !ok {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	return obj
}

// numberField returns a positive finite number field, or 0.
func numberField(msg map[string]json.RawMessage, key string) float64 {
	raw, ok := msg[key]
	if !ok {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0
	}
	if f > 0 {
		return f
	}
	return 0
}

// pickU64 returns the first positive integer among the given keys.
func pickU64(record map[string]json.RawMessage, keys ...string) uint64 {
	for _, k := range keys {
		raw, ok := record[k]
		if !ok {
			continue
		}
		var n uint64
		if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// pickNestedU64 reads a positive integer from record[key][nestedKeys...].
func pickNestedU64(record map[string]json.RawMessage, key string, nestedKeys ...string) uint64 {
	nested := objectField(record, key)
	if nested == nil {
		return 0
	}
	return pickU64(nested, nestedKeys...)
}

// max64 returns the largest of its arguments.
func max64(vals ...uint64) uint64 {
	var m uint64
	for _, v := range vals {
		if v > m {
			m = v
		}
	}
	return m
}

// saturatingSub returns a-b clamped at zero.
func saturatingSub(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return 0
}
