package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// lineKind classifies one rollout line.
type lineKind int

const (
	lineSkip     lineKind = iota
	lineSession           // turn_context / token_count / thread_settings_applied
	lineHeadless          // headless exec record carrying a usage object
)

func init() {
	sources.Register(sources.Source{
		Name:     "codex",
		Discover: discover,
		Parse:    parse,
	})
}

// discover returns every Codex rollout file, or (nil, nil) when no data
// directory exists.
func discover() ([]string, error) {
	return usageFiles()
}

// parse reads one rollout JSONL file, emitting one entry per usage record.
// Session files contribute token_count events (last_token_usage preferred,
// falling back to the delta of cumulative total_token_usage snapshots);
// headless exec logs (turn.completed / result records carrying a usage
// object) contribute one entry per record.
func parse(path string) ([]model.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	p := &parser{sessionID: sessionIDFor(usageRootOf(path), path)}
	var entries []model.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch classifyLine(line) {
		case lineSession:
			e, ok := p.sessionEvent(line)
			if ok {
				entries = append(entries, e)
			}
		case lineHeadless:
			e, ok := p.execEvent(line, fileMtime(path))
			if ok {
				entries = append(entries, e)
			}
		}
	}
	return entries, nil
}

// lineKind classifies one rollout line the way upstream codex_line_usage_kind
// does: session records (session_meta / turn_context / token_count /
// thread_settings_applied envelopes), headless exec usage records, or
// irrelevant lines.
func classifyLine(line string) lineKind {
	sessionish := strings.Contains(line, "turn_context") ||
		strings.Contains(line, "event_msg") ||
		strings.Contains(line, "token_count") ||
		strings.Contains(line, "thread_settings_applied") ||
		strings.Contains(line, "session_meta")
	if sessionish && strings.Contains(line, `"type"`) {
		return lineSession
	}
	if strings.Contains(line, `"usage":`) ||
		strings.Contains(line, `"input_tokens":`) ||
		strings.Contains(line, `"prompt_tokens":`) {
		return lineHeadless
	}
	return lineSkip
}

// fileMtime returns the file's modified time as a fallback timestamp for
// headless records that carry none, mirroring upstream file_modified_timestamp.
func fileMtime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UTC()
	}
	return time.Unix(0, 0).UTC()
}

// sessionEvent decodes one session rollout line and returns the entry for a
// usable token_count event. session_meta/turn_context/thread_settings_applied
// lines update the parser state instead.
func (p *parser) sessionEvent(line string) (model.Entry, bool) {
	var rl rolloutLine
	if err := json.Unmarshal([]byte(line), &rl); err != nil {
		return model.Entry{}, false
	}
	switch rl.Type {
	case "session_meta":
		if rl.Payload != nil && rl.Payload.CWD != nil {
			p.project = projectFromCWD(*rl.Payload.CWD)
		}
		return model.Entry{}, false
	case "turn_context":
		if m := modelFrom(rl.Payload.Model, rl.Payload.ModelName); m != "" {
			p.model = m
		}
		return model.Entry{}, false
	case "event_msg":
		return p.tokenCountEvent(rl)
	}
	return model.Entry{}, false
}

// tokenCountEvent applies upstream visit_codex_session_entry semantics:
// thread_settings_applied maintains the service tier (an absent service_tier
// key keeps the recorded one, an unrecognized value clears it); the usage
// snapshot prefers last_token_usage when the cumulative total advanced and
// otherwise diffs the totals; records whose four token buckets are all zero
// are dropped.
func (p *parser) tokenCountEvent(rl rolloutLine) (model.Entry, bool) {
	if rl.Payload == nil {
		return model.Entry{}, false
	}
	payload := rl.Payload
	if payload.PayloadType == "thread_settings_applied" {
		if payload.ThreadSettings != nil {
			if fast, recognized := serviceTier(payload.ThreadSettings.ServiceTier); recognized {
				p.fast = fast
			} else if payload.ThreadSettings.ServiceTier != nil {
				// Present but unrecognized: the tier changed to something
				// unknown, so the stale value must not be inherited.
				p.fast = false
			}
		}
		return model.Entry{}, false
	}
	if payload.PayloadType != "token_count" || payload.Info == nil {
		return model.Entry{}, false
	}
	ts, ok := parseTimestamp(rl.Timestamp)
	if !ok {
		return model.Entry{}, false
	}
	info := payload.Info
	var total, last *rawTokens
	if info.Total != nil {
		t := info.Total.normalize()
		total = &t
	}
	if info.Last != nil {
		l := info.Last.normalize()
		last = &l
	}
	cumulativeAdvanced := total == nil || !same(total, p.prevTotal)
	var raw *rawTokens
	switch {
	case last != nil && cumulativeAdvanced:
		raw = last
	case total != nil:
		d := diff(total, p.prevTotal)
		raw = &d
	}
	if total != nil {
		p.prevTotal = total
	}
	if raw == nil {
		return model.Entry{}, false
	}
	if raw.input() == 0 && raw.cached() == 0 && raw.output() == 0 && raw.ReasoningOutTokens == 0 {
		if raw.TotalTokens == 0 {
			return model.Entry{}, false
		}
		// Lone grand total with no split: xusage maps it to ExtraTotalTokens
		// (SPEC §3.2) where upstream would drop the record.
		m := p.resolveModel(modelFrom(info.Model, info.ModelName, payload.Model, payload.ModelName))
		return p.nextEntry(ts, m, p.fast, 0, 0, 0, raw.TotalTokens), true
	}
	in, cacheRead, out := buckets(raw)
	var extra uint64
	if raw.TotalTokens > 0 && in+cacheRead+out == 0 {
		extra = raw.TotalTokens
	}
	m := p.resolveModel(modelFrom(info.Model, info.ModelName, payload.Model, payload.ModelName))
	e := p.nextEntry(ts, m, p.fast, in, cacheRead, out, extra)
	if e.TotalTokens() == 0 {
		return model.Entry{}, false
	}
	return e, true
}

// resolveModel tracks the last seen model so later events inherit it, with
// upstream's "gpt-5" final fallback.
func (p *parser) resolveModel(parsed string) string {
	if parsed != "" {
		p.model = parsed
		return parsed
	}
	if p.model == "" {
		p.model = "gpt-5" // upstream's fallback model
	}
	return p.model
}

// execEvent decodes one headless exec record (turn.completed, result, or a
// bare data/response envelope) into an entry, mirroring upstream's
// add_codex_exec_event: usage/model/timestamp each resolve through the
// top-level, data, result, and response fields in that order.
func (p *parser) execEvent(line string, fallbackTS time.Time) (model.Entry, bool) {
	var rec execRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return model.Entry{}, false
	}
	usage := firstUsage(rec.Usage, rec.Data, rec.Result, rec.Response)
	if usage == nil {
		return model.Entry{}, false
	}
	u := usage.normalize()
	if u.input() == 0 && u.cached() == 0 && u.output() == 0 && u.ReasoningOutTokens == 0 && u.TotalTokens == 0 {
		return model.Entry{}, false
	}
	ts := firstTimestamp(fallbackTS, rec.Timestamp, rec.CreatedAt, rec.CreatedAtCamel,
		execTS(rec.Data), execTS(rec.Result), execTS(rec.Response))
	modelName := firstModel(rec.Model, rec.ModelName, rec.Metadata,
		rec.Data, rec.Result, rec.Response)
	in, cacheRead, out := buckets(&u)
	var extra uint64
	if u.TotalTokens > 0 && in+cacheRead+out == 0 {
		extra = u.TotalTokens
	}
	m := p.resolveModel(modelName)
	e := p.nextEntry(ts, m, false, in, cacheRead, out, extra)
	if e.TotalTokens() == 0 {
		return model.Entry{}, false
	}
	return e, true
}

// execRecord mirrors the subset of a headless exec JSONL record xusage reads.
type execRecord struct {
	Timestamp      json.RawMessage `json:"timestamp"`
	CreatedAt      json.RawMessage `json:"created_at"`
	CreatedAtCamel json.RawMessage `json:"createdAt"`
	Model          *string         `json:"model"`
	ModelName      *string         `json:"model_name"`
	Metadata       *struct {
		Model *string `json:"model"`
	} `json:"metadata"`
	Usage    *rawTokens  `json:"usage"`
	Data     *execFields `json:"data"`
	Result   *execFields `json:"result"`
	Response *execFields `json:"response"`
}

// execFields mirrors the data/result/response sub-objects of exec records.
type execFields struct {
	Timestamp      json.RawMessage `json:"timestamp"`
	CreatedAt      json.RawMessage `json:"created_at"`
	CreatedAtCamel json.RawMessage `json:"createdAt"`
	Model          *string         `json:"model"`
	ModelName      *string         `json:"model_name"`
	Metadata       *struct {
		Model *string `json:"model"`
	} `json:"metadata"`
	Usage *rawTokens `json:"usage"`
}

// firstUsage returns the first present usage object across the top-level
// record and its data/result/response sub-objects.
func firstUsage(top *rawTokens, fields ...*execFields) *rawTokens {
	if top != nil {
		return top
	}
	for _, f := range fields {
		if f != nil && f.Usage != nil {
			return f.Usage
		}
	}
	return nil
}

// execTS extracts a record sub-object's timestamp raw JSON.
func execTS(f *execFields) json.RawMessage {
	if f == nil {
		return nil
	}
	if len(f.Timestamp) > 0 {
		return f.Timestamp
	}
	if len(f.CreatedAt) > 0 {
		return f.CreatedAt
	}
	return f.CreatedAtCamel
}

// firstTimestamp parses the first usable timestamp, falling back to t.
func firstTimestamp(fallback time.Time, raws ...json.RawMessage) time.Time {
	for _, raw := range raws {
		if len(raw) == 0 {
			continue
		}
		if ts, ok := parseTimestamp(raw); ok {
			return ts
		}
	}
	return fallback
}

// firstModel returns the first non-empty model spelling across top-level and
// nested record fields.
func firstModel(ps ...any) string {
	for _, p := range ps {
		switch v := p.(type) {
		case *string:
			if s := modelFrom(v); s != "" {
				return s
			}
		case *execFields:
			if v == nil {
				continue
			}
			if s := modelFrom(v.Model, v.ModelName); s != "" {
				return s
			}
			if v.Metadata != nil {
				if s := modelFrom(v.Metadata.Model); s != "" {
					return s
				}
			}
		case *struct {
			Model *string `json:"model"`
		}:
			if v != nil {
				if s := modelFrom(v.Model); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// usageRootOf finds the sessions root (sessions/, archived_sessions/, or the
// home itself) that contains path, for session-id derivation.
func usageRootOf(path string) string {
	for _, dir := range usageDirs() {
		if strings.HasPrefix(path, dir+string(os.PathSeparator)) ||
			strings.HasPrefix(path, dir+"/") {
			return dir
		}
	}
	return filepath_Dir(path)
}

// filepath_Dir avoids importing path/filepath twice under two names.
func filepath_Dir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		c := path[i]
		if c == '/' || c == '\\' {
			return path[:i]
		}
	}
	return "."
}
