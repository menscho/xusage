package grok

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"xusage/internal/model"
)

// costUsdTicksPerUSD converts Grok's fixed-point costUsdTicks into USD: one
// tick is 1e-10 USD. The recorded ticks are the only value that survives turn
// aggregation, so they are preferred over recomputing from token counts.
const costUsdTicksPerUSD = 1e10

// updateLine is the JSONL envelope of one Grok session update record.
// Timestamp fields stay raw: Grok writes integers today but float-formatted
// numbers are legal JSON, and json.Number rejects those outright.
type updateLine struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Params    *updateParams   `json:"params"`
}

// updateParams carries the session id, the update payload, and event metadata.
type updateParams struct {
	SessionID string      `json:"sessionId"`
	Update    *updateBody `json:"update"`
	Meta      *eventMeta  `json:"_meta"`
}

// updateBody discriminates the update kind and carries per-turn usage.
type updateBody struct {
	SessionUpdate string      `json:"sessionUpdate"`
	Usage         *usageBlock `json:"usage"`
}

// eventMeta identifies the server event and refines its timestamp.
type eventMeta struct {
	EventID        string          `json:"eventId"`
	AgentTimestamp json.RawMessage `json:"agentTimestampMs"`
}

// usageBlock is one turn's aggregate usage. ModelUsage fans out per model;
// when absent, the top-level numbers describe a single default model.
type usageBlock struct {
	InputTokens         json.Number            `json:"inputTokens"`
	OutputTokens        json.Number            `json:"outputTokens"`
	CachedReadTokens    json.Number            `json:"cachedReadTokens"`
	CacheCreationTokens json.Number            `json:"cacheCreationTokens"`
	ReasoningTokens     json.Number            `json:"reasoningTokens"`
	TotalTokens         json.Number            `json:"totalTokens"`
	CostUsdTicks        json.Number            `json:"costUsdTicks"`
	ModelUsage          map[string]*modelUsage `json:"modelUsage"`
}

// modelUsage is the per-model slice of one turn's usage.
type modelUsage struct {
	InputTokens         json.Number `json:"inputTokens"`
	OutputTokens        json.Number `json:"outputTokens"`
	CachedReadTokens    json.Number `json:"cachedReadTokens"`
	CacheCreationTokens json.Number `json:"cacheCreationTokens"`
	ReasoningTokens     json.Number `json:"reasoningTokens"`
	TotalTokens         json.Number `json:"totalTokens"`
	CostUsdTicks        json.Number `json:"costUsdTicks"`
}

// summaryJSON mirrors the optional summary.json sibling of updates.jsonl.
type summaryJSON struct {
	Info *struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"info"`
	GitRootDir     string `json:"git_root_dir"`
	CurrentModelID string `json:"current_model_id"`
}

// sessionMeta is resolved once per file from summary.json and path layout.
type sessionMeta struct {
	sessionID    string
	projectPath  string
	defaultModel string
}

// loadSessionMeta derives session id, project path, and default model for one
// session directory. summary.json wins over path-derived values; the session
// directory name is the fallback id, the URL-encoded project segment the
// fallback project.
func loadSessionMeta(updatesPath string) sessionMeta {
	meta := sessionMeta{sessionID: "unknown", projectPath: "unknown"}
	dir := filepath.Dir(updatesPath)
	if dir == "." || dir == "" {
		return meta
	}
	meta.sessionID = filepath.Base(dir)
	if parent := filepath.Dir(dir); parent != dir {
		if seg := filepath.Base(parent); seg != "" && seg != "sessions" {
			meta.projectPath = urlDecodeLightweight(seg)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return meta
	}
	var s summaryJSON
	if err := json.Unmarshal(data, &s); err != nil {
		return meta
	}
	if s.Info != nil {
		if s.Info.ID != "" {
			meta.sessionID = s.Info.ID
		}
		if s.Info.CWD != "" {
			meta.projectPath = s.Info.CWD
		}
	}
	if s.Info == nil || s.Info.CWD == "" {
		if s.GitRootDir != "" {
			meta.projectPath = s.GitRootDir
		}
	}
	meta.defaultModel = s.CurrentModelID
	return meta
}

// modelRow pairs a raw model name with its per-model usage.
type modelRow struct {
	name string
	m    *modelUsage
}

// parseFile parses one updates.jsonl into usage entries. Malformed lines are
// skipped; only unreadable files warrant an error.
func parseFile(path string) ([]model.Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta := loadSessionMeta(path)
	var entries []model.Entry
	seen := make(map[string]bool) // eventId|model dedup within one file

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, `"turn_completed"`) { // fast prefilter (quoted, like upstream)
			continue
		}
		var rec updateLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // malformed lines never fail the file
		}
		if rec.Params == nil || rec.Params.Update == nil {
			continue
		}
		body := rec.Params.Update
		if strings.TrimSpace(body.SessionUpdate) != "turn_completed" || body.Usage == nil {
			continue
		}
		usage := body.Usage

		eventID := ""
		if rec.Params.Meta != nil {
			eventID = strings.TrimSpace(rec.Params.Meta.EventID)
		}
		ts := resolveTimestamp(&rec, rec.Params.Meta)
		sessionID := rec.Params.SessionID
		if sessionID == "" {
			sessionID = meta.sessionID
		}

		for _, row := range sortedModelUsage(usage, meta.defaultModel) {
			entry, ok := buildEntry(row, ts, sessionID, eventID, seen)
			if !ok {
				continue
			}
			entry.Project = meta.projectPath
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// buildEntry maps one per-model usage slice onto an Entry, applying the
// input/cache split, zero-row skipping, and within-file dedup. The second
// result is false when the row carries no information or duplicates a prior
// identical turn.
func buildEntry(row modelRow, ts time.Time, sessionID, eventID string, seen map[string]bool) (model.Entry, bool) {
	in := numU64(row.m.InputTokens)
	out := numU64(row.m.OutputTokens)
	cacheRead := numU64(row.m.CachedReadTokens)
	cacheCreate := numU64(row.m.CacheCreationTokens)
	reasoning := numU64(row.m.ReasoningTokens)

	// Split OpenAI-style input that includes cache: cached is a subset of
	// input; cache creation carves out of what remains, keeping the three
	// buckets summing back to the reported input.
	cached := min(cacheRead, in)
	uncached := in - cached
	createClamped := min(cacheCreate, uncached)
	uncached -= createClamped

	if uncached == 0 && cached == 0 && createClamped == 0 && out == 0 && reasoning == 0 {
		return model.Entry{}, false // zero rows carry no information
	}

	dedupKey := sessionID + "|" + strconv.FormatInt(ts.UnixMilli(), 10) + "|" + row.name +
		"|" + strconv.FormatUint(uncached, 10) + "|" + strconv.FormatUint(out, 10) +
		"|" + strconv.FormatUint(cached, 10) + "|" + strconv.FormatUint(createClamped, 10) +
		"|" + strconv.FormatUint(reasoning, 10) +
		"|" + strconv.FormatUint(reasoning, 10)
	if eventID != "" {
		dedupKey = eventID + "|" + row.name
	}
	if seen[dedupKey] {
		return model.Entry{}, false
	}
	seen[dedupKey] = true

	entry := model.Entry{
		ID:                  eventID,
		RequestID:           eventID,
		Timestamp:           ts,
		Model:               row.name,
		RawModel:            row.name,
		SessionID:           sessionID,
		InputTokens:         uncached,
		OutputTokens:        out,
		CacheCreationTokens: createClamped,
		CacheReadTokens:     cached,
		ExtraTotalTokens:    0, // reasoning already sits inside output
		Messages:            1,
	}
	if ticks := numU64(row.m.CostUsdTicks); ticks > 0 {
		cost := float64(ticks) / costUsdTicksPerUSD
		entry.ReportedCost = &cost
	}
	return entry, true
}

// sortedModelUsage yields (name, usage) pairs deterministically: the modelUsage
// map when present, otherwise a single row for the default model (or unknown).
func sortedModelUsage(u *usageBlock, defaultModel string) []modelRow {
	if len(u.ModelUsage) > 0 {
		names := make([]string, 0, len(u.ModelUsage))
		for name := range u.ModelUsage {
			names = append(names, name)
		}
		sort.Strings(names)
		rows := make([]modelRow, 0, len(names))
		for _, name := range names {
			if mu := u.ModelUsage[name]; mu != nil {
				rows = append(rows, modelRow{name, mu})
			}
		}
		return rows
	}
	name := defaultModel
	if name == "" {
		name = "unknown"
	}
	fallback := &modelUsage{
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CachedReadTokens:    u.CachedReadTokens,
		CacheCreationTokens: u.CacheCreationTokens,
		ReasoningTokens:     u.ReasoningTokens,
		TotalTokens:         u.TotalTokens,
		CostUsdTicks:        u.CostUsdTicks,
	}
	return []modelRow{{name, fallback}}
}

// resolveTimestamp prefers _meta.agentTimestampMs (milliseconds), then the
// envelope timestamp (Unix seconds), then the Unix epoch.
func resolveTimestamp(rec *updateLine, meta *eventMeta) time.Time {
	if meta != nil {
		if ms, ok := numI64(meta.AgentTimestamp); ok && ms > 0 {
			return time.UnixMilli(ms).UTC()
		}
	}
	if rec.Timestamp != nil {
		if secs, ok := numI64(rec.Timestamp); ok && secs > 0 {
			return time.Unix(secs, 0).UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

// numU64 parses a lenient unsigned token count; anything unusual is zero.
func numU64(n json.Number) uint64 {
	if n == "" {
		return 0
	}
	if v, err := strconv.ParseUint(n.String(), 10, 64); err == nil {
		return v
	}
	return 0
}

// numI64 parses a lenient signed integer timestamp field. JSON decoders hand
// us float64 even for integral values, so the fractional form is accepted and
// truncated; non-finite input is rejected. Mirrors upstream value_as_i64
// (as_i64, then as_u64, then as_f64 truncation).
func numI64(raw json.RawMessage) (int64, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return 0, false
	}
	if v, err := strconv.ParseInt(t, 10, 64); err == nil {
		return v, true
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return int64(f), true
}

// urlDecodeLightweight decodes percent-encoded path segments such as
// `D%3A%5Cproj`. Decoding happens on bytes so multi-byte UTF-8 sequences split
// across triplets recombine correctly; invalid escapes and bytes that do not
// form valid UTF-8 degrade to the Unicode replacement character.
func urlDecodeLightweight(value string) string {
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) {
			hi, okHi := fromHex(value[i+1])
			lo, okLo := fromHex(value[i+2])
			if okHi && okLo {
				out = append(out, hi*16+lo)
				i += 2
				continue
			}
		}
		out = append(out, value[i])
	}
	if !utf8.Valid(out) {
		return strings.ToValidUTF8(string(out), "\uFFFD")
	}
	return string(out)
}

// fromHex converts one hex digit to its value.
func fromHex(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	default:
		return 0, false
	}
}
