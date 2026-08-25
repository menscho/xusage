package kimi

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/sources/adapter"
)

const (
	defaultModel    = "kimi-for-coding"
	statusUpdateMsg = "StatusUpdate"
	usageRecordType = "usage.record"
	metadataType    = "metadata"
	agentsDir       = "agents"
	configFileName  = "config.json"
	// kimiForCodingCutoffMs is the instant after which the "kimi-for-coding"
	// plan maps to kimi-k2.6 instead of kimi-k2.5 for pricing candidates.
	kimiForCodingCutoffMs int64 = 1_776_698_890_072
)

// wireLine is the subset of a Kimi wire JSONL line we consume.
type wireLine struct {
	Type       string       `json:"type"`
	Timestamp  *float64     `json:"timestamp"`
	Message    *wireMessage `json:"message"`
	Model      *string      `json:"model"`
	Time       *int64       `json:"time"`
	Usage      *newUsage    `json:"usage"`
	UsageScope string       `json:"usageScope"`
}

// wireMessage covers both old-format message envelopes and the bare payload
// object some builds write directly at top level.
type wireMessage struct {
	Type    string          `json:"type"`
	Payload *wirePayloadRaw `json:"payload"`
}

type wirePayloadRaw struct {
	TokenUsage *tokenUsage `json:"token_usage"`
	MessageID  *string     `json:"message_id"`
}

type tokenUsage struct {
	InputOther       uint64 `json:"input_other"`
	Output           uint64 `json:"output"`
	InputCacheCreate uint64 `json:"input_cache_creation"`
	InputCacheRead   uint64 `json:"input_cache_read"`
	Total            uint64 `json:"total"`
}

// newUsage is the camelCase usage block of Kimi Code's "usage.record" lines.
type newUsage struct {
	InputOther       uint64 `json:"inputOther"`
	Output           uint64 `json:"output"`
	InputCacheCreate uint64 `json:"inputCacheCreation"`
	InputCacheRead   uint64 `json:"inputCacheRead"`
}

// parsedEntry carries one extracted usage event before model.Entry assembly.
type parsedEntry struct {
	ts          time.Time
	sessionID   string
	model       string
	messageID   string
	input       uint64
	output      uint64
	cacheCreate uint64
	cacheRead   uint64
	extraTotal  uint64
}

// Parse extracts usage entries from one wire.jsonl file.
//
// Old format lines are StatusUpdate messages carrying token_usage; consecutive
// updates collapse to the latest (each update reports cumulative turn usage).
// New format lines are type=usage.record with usageScope=turn only --
// session-scoped records are running totals and must not double count.
func Parse(path string) ([]model.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fallback := fileModTime(path)
	cfgModel := adapter.Intern(readConfiguredModel(path))
	sessID := sessionIDFromPath(path)

	var out []model.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" ||
			(!strings.Contains(line, `"token_usage"`) &&
				!strings.Contains(line, `"usage.record"`) &&
				!strings.Contains(line, `"usage"`)) {
			continue
		}
		e, ok := parseLine(line, fallback, cfgModel, sessID)
		if ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// parseLine converts one JSONL line into an entry; ok=false skips the line.
func parseLine(line string, fallback time.Time, cfgModel, sessID string) (model.Entry, bool) {
	var wl wireLine
	if err := jsonUnmarshal(line, &wl); err != nil {
		return model.Entry{}, false
	}

	switch wl.Type {
	case metadataType:
		return model.Entry{}, false
	case usageRecordType:
		return newFormatEntry(&wl, fallback, sessID)
	default:
		return oldFormatEntry(&wl, fallback, cfgModel, sessID)
	}
}

// oldFormatEntry handles {"message":{"type":"StatusUpdate","payload":
// {"token_usage":...,"message_id":...}}} lines. Upstream requires the
// StatusUpdate envelope; a bare top-level payload is not an old-format record.
func oldFormatEntry(wl *wireLine, fallback time.Time, cfgModel, sessID string) (model.Entry, bool) {
	if wl.Message == nil || wl.Message.Type != statusUpdateMsg {
		return model.Entry{}, false
	}
	payload := wl.Message.Payload
	if payload == nil || payload.TokenUsage == nil {
		return model.Entry{}, false
	}
	tu := payload.TokenUsage
	in, out, cc, cr, extra := splitTokens(tu.InputOther, tu.Output,
		tu.InputCacheCreate, tu.InputCacheRead, tu.Total)
	if in+out+cc+cr+extra == 0 {
		return model.Entry{}, false
	}
	ts := fallback
	if wl.Timestamp != nil && isFinite(*wl.Timestamp) {
		ts = time.UnixMilli(int64(*wl.Timestamp * 1000))
	}
	return model.Entry{
		ID:                  messageID(payload),
		Timestamp:           ts.UTC(),
		RawModel:            cfgModel,
		Model:               cfgModel,
		SessionID:           sessID,
		InputTokens:         in,
		OutputTokens:        out,
		CacheCreationTokens: cc,
		CacheReadTokens:     cr,
		ExtraTotalTokens:    extra,
	}, true
}

// messageID mirrors upstream non_empty_string on payload.message_id: an empty
// string is absent, so the entry carries no id (and never dedups).
func messageID(p *wirePayloadRaw) string {
	if p == nil || p.MessageID == nil {
		return ""
	}
	return *p.MessageID
}

// newFormatEntry handles Kimi Code "usage.record" turn records.
func newFormatEntry(wl *wireLine, fallback time.Time, sessID string) (model.Entry, bool) {
	if !strings.EqualFold(wl.UsageScope, "turn") {
		return model.Entry{}, false
	}
	u := wl.Usage
	if u == nil {
		return model.Entry{}, false
	}
	in, out, cc, cr, extra := splitTokens(u.InputOther, u.Output,
		u.InputCacheCreate, u.InputCacheRead, 0)
	if in+out+cc+cr+extra == 0 {
		return model.Entry{}, false
	}
	ts := fallback
	if wl.Time != nil && *wl.Time >= 0 {
		ts = time.UnixMilli(*wl.Time)
	}
	rawModel := defaultModel
	m := defaultModel
	if wl.Model != nil {
		if trimmed := strings.TrimSpace(*wl.Model); trimmed != "" {
			rawModel = trimmed
			m = strings.TrimPrefix(trimmed, "kimi-code/")
		}
	}
	return model.Entry{
		Timestamp:           ts.UTC(),
		RawModel:            adapter.Intern(rawModel),
		Model:               adapter.Intern(m),
		SessionID:           sessID,
		InputTokens:         in,
		OutputTokens:        out,
		CacheCreationTokens: cc,
		CacheReadTokens:     cr,
		ExtraTotalTokens:    extra,
	}, true
}

// splitTokens normalizes kimi buckets onto Entry fields, porting ccusage-core's
// apply_total_token_fallback: a grand total larger than the known split parts
// backfills zero output first, and any remaining gap lands in ExtraTotalTokens.
func splitTokens(inputOther, output, cacheCreate, cacheRead, total uint64) (in, out, cc, cr, extra uint64) {
	in, out, cc, cr = inputOther, output, cacheCreate, cacheRead
	known := in + out + cc + cr
	if total <= known {
		return in, out, cc, cr, 0
	}
	missing := total - known
	if out == 0 {
		out = missing
	} else {
		extra = missing
	}
	return in, out, cc, cr, extra
}

// readConfiguredModel reads the display model from the data root's config.json
// ("old format" lines carry no per-line model). Falls back to kimi-for-coding.
func readConfiguredModel(wirePath string) string {
	root, ok := dataRootFromWirePath(wirePath)
	if !ok {
		return defaultModel
	}
	data, err := os.ReadFile(filepath.Join(root, configFileName))
	if err != nil {
		return defaultModel
	}
	var cfg struct {
		Model string `json:"model"`
	}
	if jsonUnmarshal(string(data), &cfg) != nil || strings.TrimSpace(cfg.Model) == "" {
		return defaultModel
	}
	return strings.TrimSpace(cfg.Model)
}

// dataRootFromWirePath walks back from a wire.jsonl path to the kimi root:
// root/sessions/<group>/<session>/wire.jsonl (4 levels up) or
// root/sessions/<ws>/<session>/agents/<agent>/wire.jsonl (5 levels up).
func dataRootFromWirePath(wirePath string) (string, bool) {
	levels := 4
	if filepath.Base(filepath.Dir(filepath.Dir(wirePath))) == agentsDir {
		levels = 5
	}
	p := wirePath
	for i := 0; i < levels; i++ {
		next := filepath.Dir(p)
		if next == p {
			return "", false
		}
		p = next
	}
	return p, true
}

// sessionIDFromPath extracts the session directory name from either layout.
func sessionIDFromPath(wirePath string) string {
	dir := filepath.Dir(wirePath)
	if filepath.Base(filepath.Dir(dir)) == agentsDir {
		dir = filepath.Dir(filepath.Dir(dir))
	}
	name := filepath.Base(dir)
	if name == "." || name == string(filepath.Separator) || name == "/" {
		return "unknown"
	}
	return name
}

func fileModTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Unix(0, 0)
	}
	return fi.ModTime()
}

// NaN fails every comparison; +-Inf overflow the int64 millisecond range.
func isFinite(f float64) bool {
	return f == f && f >= -9.2e18 && f <= 9.2e18
}

// jsonUnmarshal decodes one JSON object; defined here to keep encoding/json
// usage next to the wire types it feeds.
func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// pricingCandidateModels lists lookup names for a kimi entry, mirroring
// upstream: the timestamp-dependent plan mapping first, then provider-scoped
// and bare spellings. The engine prices Entry.Model directly, so this
// package-visible helper documents candidate order for tests.
func pricingCandidateModels(m string, tsMs int64) []string {
	cands := []string{}
	if m == defaultModel {
		if tsMs < kimiForCodingCutoffMs {
			cands = append(cands, "moonshot/kimi-k2.5")
		} else {
			cands = append(cands, "moonshot/kimi-k2.6")
		}
	}
	cands = append(cands, "moonshot/"+m, "kimi/"+m, m)
	seen := map[string]bool{}
	out := cands[:0]
	for _, c := range cands {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}
