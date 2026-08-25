// Package pricing resolves model names to per-token prices and computes costs.
//
// The active pricing table is the embedded LiteLLM snapshot
// (snapshot/litellm_pricing.json) overlaid with a live snapshot fetched from
// LiteLLM (see Refresh in live.go) and any user overrides. The embedded snapshot
// is always loaded first and is the offline fallback when the live fetch fails.
// Lookup is exact first, then falls back to a separator-normalized key, a
// stripped date suffix, and finally a longest-prefix match — mirroring how
// providers spell the same model differently across logs.
package pricing

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
)

//go:embed snapshot/litellm_pricing.json
var snapshotJSON []byte

// Rates are per-token USD prices for one model.
type Rates struct {
	Input       float64
	Output      float64
	CacheCreate float64 // defaults to Input * 1.25 when absent
	CacheRead   float64 // defaults to Input * 0.1 when absent

	InputAbove200k       float64 // 0 = no long-context tier
	OutputAbove200k      float64
	CacheCreateAbove200k float64
	CacheReadAbove200k   float64

	FastMultiplier float64 // applied to the whole cost when speed==fast; 1.0 default
}

// entry mirrors the LiteLLM JSON schema.
type entry struct {
	InputCostPerToken                  *float64 `json:"input_cost_per_token"`
	OutputCostPerToken                 *float64 `json:"output_cost_per_token"`
	CacheCreationInputTokenCost        *float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost            *float64 `json:"cache_read_input_token_cost"`
	InputCostPerTokenAbove200k         *float64 `json:"input_cost_per_token_above_200k_tokens"`
	OutputCostPerTokenAbove200k        *float64 `json:"output_cost_per_token_above_200k_tokens"`
	CacheCreationInputTokenCostAbove2k *float64 `json:"cache_creation_input_token_cost_above_200k_tokens"`
	CacheReadInputTokenCostAbove200k   *float64 `json:"cache_read_input_token_cost_above_200k_tokens"`
	FastMultiplier                     *float64 `json:"fast_multiplier"`

	ProviderSpecific *providerSpecificEntry `json:"provider_specific_entry,omitempty"`
}

// providerSpecificEntry carries provider-specific rate adjustments; today only
// the "fast" service-tier multiplier is consumed.
type providerSpecificEntry struct {
	Fast *float64 `json:"fast,omitempty"`
}

// Table holds resolved rates keyed by raw LiteLLM model name.
//
// norm is a precomputed index keyed by normalize(rawName) and built once at
// load. It lets Find resolve the common exact-match case with a single
// normalize() of the input plus one map probe, instead of materializing the
// full candidateKeys spelling list per lookup. normalize() collisions are rare
// in the snapshot and only ever join keys that carry identical rates (verified),
// so first-seen-wins is value-preserving and the index is deterministic.
type Table struct {
	rates map[string]Rates
	norm  map[string]Rates
}

var current atomic.Pointer[Table]

func init() {
	SetTable(nil) // embedded snapshot
}

// SetTable replaces the active table from raw LiteLLM JSON. A nil or invalid
// payload keeps or restores the embedded snapshot. Custom overrides from the
// override file are applied on top in every case. It is safe for concurrent use.
func SetTable(litellmJSON []byte) error {
	return applyAll(litellmJSON)
}

func loadInto(t *Table, data []byte) error {
	var raw map[string]entry
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Pre-size on the first (snapshot) load so the 2610-entry fill doesn't
	// trigger repeated map grow/rehash. The overlay pass starts from a
	// non-empty map, so it skips this and grows only for its own (few) entries.
	if len(t.rates) == 0 {
		t.rates = make(map[string]Rates, len(raw))
	}
	for name, e := range raw {
		if e.InputCostPerToken == nil || e.OutputCostPerToken == nil {
			continue
		}
		in := *e.InputCostPerToken
		out := *e.OutputCostPerToken
		cc := in * 1.25
		if e.CacheCreationInputTokenCost != nil {
			cc = *e.CacheCreationInputTokenCost
		}
		cr := in * 0.1
		if e.CacheReadInputTokenCost != nil {
			cr = *e.CacheReadInputTokenCost
		}
		r := Rates{
			Input:       in,
			Output:      out,
			CacheCreate: cc,
			CacheRead:   cr,
		}
		if e.InputCostPerTokenAbove200k != nil {
			r.InputAbove200k = *e.InputCostPerTokenAbove200k
		}
		if e.OutputCostPerTokenAbove200k != nil {
			r.OutputAbove200k = *e.OutputCostPerTokenAbove200k
		}
		if e.CacheCreationInputTokenCostAbove2k != nil {
			r.CacheCreateAbove200k = *e.CacheCreationInputTokenCostAbove2k
		}
		if e.CacheReadInputTokenCostAbove200k != nil {
			r.CacheReadAbove200k = *e.CacheReadInputTokenCostAbove200k
		}
		if e.ProviderSpecific != nil && e.ProviderSpecific.Fast != nil {
			r.FastMultiplier = *e.ProviderSpecific.Fast
		}
		if e.FastMultiplier != nil {
			r.FastMultiplier = *e.FastMultiplier // explicit top-level wins
		}
		t.rates[name] = r
	}
	// Precompute the normalized-key index for allocation-free exact lookup.
	// Rebuild from the current rates each load so overlays are included.
	t.indexNorm()
	return nil
}

// indexNorm builds norm, a normalize(rawName)->rates index. Canonical keys
// (normalize(name)==name) are indexed first so a normalized form that is itself
// a raw key resolves to that exact key's rates; non-canonical spellings then
// fill only the remaining gaps. Collisions only ever join equal-rate keys, so
// first-seen-wins preserves the (Rates, bool) result of the old ordered scan.
func (t *Table) indexNorm() {
	t.norm = make(map[string]Rates, len(t.rates))
	for name, r := range t.rates {
		if normalize(name) == name {
			t.norm[name] = r
		}
	}
	for name, r := range t.rates {
		n := normalize(name)
		if n != name {
			if _, ok := t.norm[n]; !ok {
				t.norm[n] = r
			}
		}
	}
}

// Get returns the active table.
func Get() *Table { return current.Load() }

// Find resolves a model name to rates. Aliases from XUSAGE_MODEL_ALIASES apply
// first (JSON map or "a=b,c=d" list); "-fast" suffixed models inherit the base
// alias's rates. Returns false when no pricing is known.
func (t *Table) Find(model string) (Rates, bool) {
	if t == nil {
		return Rates{}, false
	}
	if alias := aliasFor(model); alias != "" {
		model = alias
	}
	fast := false
	if base, ok := strings.CutSuffix(model, "-fast"); ok {
		model, fast = base, true
	}

	// Common path — one normalize() plus one map probe, no candidateKeys
	// allocation. indexNorm indexes every key in rates by its normalized form,
	// so any raw or lowercased hit is covered here; the t.rates probes that
	// followed in the old ordered scan are unreachable (if model were in rates,
	// its normalized form is already in norm) and are omitted.
	norm := normalize(model)
	if r, ok := t.norm[norm]; ok {
		return withFast(r, fast), true
	}
	// Rare spelling variants: stripped date suffix, prefix walks, dotted
	// provider segments. candidateKeys allocates; only reached on miss above.
	for _, key := range candidateKeys(model) {
		if r, ok := t.rates[key]; ok {
			return withFast(r, fast), true
		}
	}
	// A "-fast" query whose stripped form is unknown may still match a table
	// key that itself ends in "-fast" (e.g. a "claude-opus-4-8-fast" entry).
	if fast {
		for _, key := range candidateKeys(model + "-fast") {
			if r, ok := t.rates[key]; ok {
				return r, true
			}
		}
	}
	return Rates{}, false
}

// withFast applies the default 1.0 fast multiplier when the request is fast and
// neither the table entry nor a "-fast"-suffixed sibling defined one.
func withFast(r Rates, fast bool) Rates {
	if fast && r.FastMultiplier == 0 {
		r.FastMultiplier = 1.0
	}
	return r
}

// Known reports whether any rates resolve for the model.
func (t *Table) Known(model string) bool {
	_, ok := t.Find(model)
	return ok
}

// candidateKeys yields lookup keys in priority order: every spelling variant
// (raw, lowercased, separator-normalized) tried exactly and with an 8-digit
// date suffix stripped, then progressively shorter prefix candidates cut on
// segment boundaries, longest first. Trying all exact/date-stripped spellings
// before any prefix keeps a faithful match ahead of a looser one.
func candidateKeys(model string) []string {
	keys := make([]string, 0, 8)
	add := func(s string) {
		if s != "" && !slices.Contains(keys, s) {
			keys = append(keys, s)
		}
	}
	bases := []string{model}
	if lower := strings.ToLower(model); lower != model {
		bases = append(bases, lower)
	}
	if norm := normalize(model); norm != model {
		add(norm)
		bases = append(bases, norm)
	}
	// Phase 1: whole-key candidates, exact then date-suffix-stripped.
	for _, b := range bases {
		add(b)
	}
	for _, b := range bases {
		add(stripDateSuffix(b))
	}
	// Phase 2: longest-prefix match on segment boundaries. Also cut leading
	// provider segments off the raw spelling ("bedrock.anthropic.claude-…"
	// → "anthropic.claude-…") so dotted provider spellings reach keys stored
	// under their bare dotted form.
	for _, b := range bases {
		for prefix := b; ; {
			i := strings.LastIndexAny(prefix, "-/:@.")
			if i <= 0 {
				break
			}
			prefix = prefix[:i]
			add(prefix)
		}
	}
	for _, b := range bases {
		parts := strings.SplitN(b, ".", 3) // bedrock | anthropic | rest
		if len(parts) == 3 {
			rest := parts[1] + "." + parts[2]
			add(rest)
			add(stripDateSuffix(rest))
			// Also drop every leading dotted segment: "x.y.claude-…-5" must
			// reach keys stored under their bare dotted or dash form.
			for r := rest; ; {
				dot := strings.Index(r, ".")
				if dot < 0 {
					break
				}
				r = r[dot+1:]
				add(r)
				add(stripDateSuffix(r))
			}
		}
	}
	return keys
}

func normalize(model string) string {
	// Fast path: if the name is already lowercase ASCII with no '.' or '@',
	// it needs no transformation — return as-is to skip the ToLower and
	// ReplaceAll allocations on the lookup hot path. Non-ASCII bytes (>127)
	// fall through so Unicode case folding stays correct.
	needs := false
	for i := 0; i < len(model); i++ {
		c := model[i]
		if c >= 'A' && c <= 'Z' || c == '.' || c == '@' || c > 127 {
			needs = true
			break
		}
	}
	if !needs {
		return model
	}
	s := strings.ToLower(model)
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "@", "-")
	return s
}

// stripDateSuffix removes an 8-digit trailing YYYYMMDD-style suffix and any
// separator left dangling before it.
func stripDateSuffix(s string) string {
	if len(s) < 9 || s[len(s)-8:] == "" {
		return s
	}
	digits := s[len(s)-8:]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return s
		}
	}
	return strings.TrimRight(s[:len(s)-8], "-/:@._")
}

// aliasFor resolves XUSAGE_MODEL_ALIASES (parsed lazily, re-parsed when the
// env var changes). "-fast" suffixed models inherit the base alias.
func aliasFor(model string) string {
	if v := lookupAlias(model); v != "" {
		return v
	}
	if base, ok := strings.CutSuffix(model, "-fast"); ok {
		if v := lookupAlias(base); v != "" {
			return v + "-fast"
		}
	}
	return ""
}

// Cost computes the USD cost of one usage event under the given rates,
// applying long-context tiers marginally at the 200k default threshold and
// the fast multiplier when speedFast is set.
func (r Rates) Cost(input, output, cacheCreate5m, cacheCreate1h, cacheRead uint64, speedFast bool) float64 {
	const threshold = 200_000
	in1hRate := r.Input * 2.0
	in1hAbove := 0.0
	if r.InputAbove200k > 0 {
		in1hAbove = r.InputAbove200k * 2.0
	}
	cost := tiered(input, r.Input, r.InputAbove200k, threshold) +
		tiered(output, r.Output, r.OutputAbove200k, threshold) +
		tiered(cacheCreate5m, r.CacheCreate, r.CacheCreateAbove200k, threshold) +
		tiered(cacheCreate1h, in1hRate, in1hAbove, threshold) +
		tiered(cacheRead, r.CacheRead, r.CacheReadAbove200k, threshold)
	if speedFast && r.FastMultiplier > 0 {
		cost *= r.FastMultiplier
	}
	return cost
}

// CostOf is a convenience wrapper: find rates for model, then compute.
// Unknown models cost nothing.
func CostOf(model string, input, output, cc5m, cc1h, cacheRead uint64, speedFast bool) float64 {
	r, ok := Get().Find(model)
	if !ok {
		return 0
	}
	return r.Cost(input, output, cc5m, cc1h, cacheRead, speedFast)
}

func tiered(tokens uint64, base, above float64, threshold uint64) float64 {
	if tokens == 0 {
		return 0
	}
	if above > 0 && tokens > threshold {
		return float64(threshold)*base + float64(tokens-threshold)*above
	}
	return float64(tokens) * base
}
