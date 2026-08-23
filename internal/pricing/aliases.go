package pricing

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// parseAliases parses XUSAGE_MODEL_ALIASES: either a JSON object
// {"from":"to"} or a comma/semicolon/newline-separated "from=to" list.
func parseAliases(raw string) map[string]string {
	out := map[string]string{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return out
	}
	if strings.HasPrefix(trimmed, "{") {
		// Lenient JSON: values may be non-strings; keep only string fields.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			for k, v := range obj {
				var s string
				if k != "" && json.Unmarshal(v, &s) == nil && s != "" {
					out[k] = s
				}
			}
			return out
		}
		// Malformed JSON falls through to the delimited-list form, mirroring
		// upstream parse_model_aliases.
	}
	// A JSON object followed by trailing list pairs (e.g. `{"a":"b"},c=d`)
	// parses as JSON for the object part; the dispatch test pins that the
	// object wins and the trailing pairs are ignored.
	if idx := strings.Index(trimmed, "}"); idx >= 0 && strings.HasPrefix(trimmed, "{") && idx < len(trimmed)-1 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed[:idx+1]), &obj); err == nil {
			for k, v := range obj {
				var s string
				if k != "" && json.Unmarshal(v, &s) == nil && s != "" {
					out[k] = s
				}
			}
			return out
		}
	}
	stripped := strings.TrimSuffix(strings.TrimPrefix(trimmed, "{"), "}")
	for _, pair := range strings.FieldsFunc(stripped, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if ok && k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

// aliasCache memoizes the parsed XUSAGE_MODEL_ALIASES value. The cache is
// keyed on the raw env string so tests (and live env changes) take effect on
// the next lookup instead of being pinned by sync.Once.
var (
	aliasMu     sync.RWMutex
	aliasRaw    string
	aliasParsed bool
	aliasMap    map[string]string
)

// resetAliasCache drops the memoized alias table; used by tests.
func resetAliasCache() {
	aliasMu.Lock()
	defer aliasMu.Unlock()
	aliasRaw, aliasParsed, aliasMap = "", false, nil
}

// lookupAlias returns the alias target for model, or "" when none applies.
func lookupAlias(model string) string {
	raw := os.Getenv("XUSAGE_MODEL_ALIASES")
	aliasMu.RLock()
	cached := aliasParsed && aliasRaw == raw
	m := aliasMap
	aliasMu.RUnlock()
	if !cached {
		m = parseAliases(raw)
		aliasMu.Lock()
		aliasRaw, aliasParsed, aliasMap = raw, true, m
		aliasMu.Unlock()
	}
	if m == nil {
		return ""
	}
	return m[model]
}
