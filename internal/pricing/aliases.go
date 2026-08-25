package pricing

import (
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
)

// parseAliases parses XUSAGE_MODEL_ALIASES: either a JSON object
// {"from":"to"} or a comma/semicolon/newline-separated "from=to" list.
// Keys and values are deliberately NOT interned: the alias table is
// config-sized (a handful of pairs), its targets are matched against the
// pricing table's own non-interned keys, and Go map lookup is value-based —
// interning would only pin permanent, useless entries in the global intern
// table. Parsing runs only on an env change, so this stays off the hot path.
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

// aliasCache is the frozen, immutable alias table published once parsed. The
// map is never mutated after construction, so it can be shared lock-free via
// the atomic pointer below — readers pay only a single atomic load instead of
// taking the RWMutex on every lookup.
type aliasCache struct {
	raw string
	m   map[string]string
}

var aliasStore atomic.Pointer[aliasCache]

// envAliasesState tracks whether XUSAGE_MODEL_ALIASES has ever been observed
// as set. The common case is that the variable stays unset for the whole
// process lifetime; probing it on every lookup still costs os.Getenv, which
// allocates on every call (2 allocs/op on Windows) even when the variable is
// absent. After the first lookup the state settles to either aliasesEmpty
// (never call os.Getenv again — zero-cost steady state) or aliasesSet (probe
// each lookup so config changes are still picked up). The zero value means
// "unprobed".
var envAliasesState atomic.Uint32

const (
	aliasesUnprobed = iota
	aliasesEmpty
	aliasesSet
)

// resetAliasCache drops all memoized alias state; used by tests after they
// change XUSAGE_MODEL_ALIASES. It resets both the parsed-alias table and the
// env-set probe so a later lookup re-probes from scratch.
func resetAliasCache() {
	envAliasesState.Store(aliasesUnprobed)
	aliasStore.Store(nil)
}

// lookupAlias returns the alias target for model, or "" when none applies.
func lookupAlias(model string) string {
	var raw string
	switch state := envAliasesState.Load(); state {
	case aliasesSet:
		raw = os.Getenv("XUSAGE_MODEL_ALIASES")
	case aliasesUnprobed:
		raw = os.Getenv("XUSAGE_MODEL_ALIASES")
		if raw != "" {
			envAliasesState.Store(aliasesSet)
		} else {
			envAliasesState.Store(aliasesEmpty)
		}
	}
	// aliasesEmpty: raw stays "" — zero-cost steady state; os.Getenv never called.
	cur := aliasStore.Load()
	if cur == nil || cur.raw != raw {
		m := parseAliases(raw)
		cur = &aliasCache{raw: raw, m: m}
		aliasStore.Store(cur)
	}
	if cur.m == nil {
		return ""
	}
	return cur.m[model]
}
