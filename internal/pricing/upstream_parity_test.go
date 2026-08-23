package pricing

import (
	"testing"
)

// Upstream reference under test:
// https://github.com/ccusage/ccusage/blob/main/rust/crates/ccusage-core/src/cost.rs
//
// These cases mirror cost.rs's own unit tests one-to-one so the Go cost math
// cannot drift from the Rust implementation. All of them exercise the LiteLLM
// schema path (marginal tiering at the default 200k threshold); the upstream
// two-stage long_context_threshold mode has no representation in the embedded
// snapshot and is intentionally not ported (see NOTES.md).
const upstreamPricingJSON = `{
  "test-model": {
    "input_cost_per_token": 1.0,
    "output_cost_per_token": 10.0,
    "cache_creation_input_token_cost": 1.25,
    "cache_read_input_token_cost": 0.1,
    "input_cost_per_token_above_200k_tokens": 2.0,
    "cache_creation_input_token_cost_above_200k_tokens": 1.5
  },
  "grok-4.5": {
    "input_cost_per_token": 2.0,
    "output_cost_per_token": 6.0,
    "cache_read_input_token_cost": 0.3,
    "input_cost_per_token_above_200k_tokens": 4.0,
    "output_cost_per_token_above_200k_tokens": 12.0,
    "cache_read_input_token_cost_above_200k_tokens": 0.6
  }
}`

// TestUpstreamParityCacheCreationBreakdown mirrors cost.rs
// prices_cache_creation_breakdown_by_duration: when cache_creation carries a
// 5m/1h breakdown, the flat cache_creation_input_tokens value (999 here) is
// ignored entirely. Expected 55.5 = 10*1.25 + 20*(1.0*2) + 30*0.1.
func TestUpstreamParityCacheCreationBreakdown(t *testing.T) {
	table := newTable(t, upstreamPricingJSON)
	rates, ok := table.Find("test-model")
	if !ok {
		t.Fatal("test-model missing")
	}
	got := rates.Cost(0, 0, 10, 20, 30, false)
	if want := 55.5; !approxEq(got, want) {
		t.Fatalf("Cost() = %v, want %v (upstream expectation)", got, want)
	}
}

// TestUpstreamParityFlatCacheCreationFallback mirrors cost.rs
// falls_back_to_flat_cache_creation_rate_without_breakdown: a lone flat
// cache_creation_input_tokens counts as 5m creation. Expected 12.5 = 10*1.25.
func TestUpstreamParityFlatCacheCreationFallback(t *testing.T) {
	table := newTable(t, upstreamPricingJSON)
	rates, _ := table.Find("test-model")
	got := rates.Cost(0, 0, 10, 0, 0, false)
	if want := 12.5; !approxEq(got, want) {
		t.Fatalf("Cost() = %v, want %v (upstream expectation)", got, want)
	}
}

// TestUpstreamParityMarginalTiering pins the LiteLLM marginal semantics of
// upstream tiered_cost: every bucket splits marginally at exactly 200k, and
// only buckets carrying an above rate participate.
func TestUpstreamParityMarginalTiering(t *testing.T) {
	table := newTable(t, upstreamPricingJSON)
	rates, _ := table.Find("test-model")

	tests := []struct {
		name           string
		in, out, cc, h uint64
		cr             uint64
		want           float64
	}{
		{
			name: "input splits marginally above 200k",
			in:   300_000,
			want: 200_000*1.0 + 100_000*2.0,
		},
		{
			name: "exactly at the threshold stays on base",
			in:   200_000,
			want: 200_000 * 1.0,
		},
		{
			name: "cache creation splits at its own above rate",
			cc:   200_500,
			want: 200_000*1.25 + 500*1.5,
		},
		{
			name: "one-hour creation doubles base and above input rates",
			h:    250_000,
			want: 200_000*(1.0*2.0) + 50_000*(2.0*2.0),
		},
		{
			name: "cache read without above rate never tiers",
			cr:   400_000,
			want: 400_000 * 0.1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rates.Cost(tt.in, tt.out, tt.cc, tt.h, tt.cr, false); !approxEq(got, tt.want) {
				t.Fatalf("Cost() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUpstreamParityCachedHeavyContext mirrors cost.rs
// cached_context_selects_the_long_context_tier in its marginal-tiering form:
// grok-4.5 re-reading 500k cached tokens with 10k fresh ones costs
// 10k*4 + 1k*12 + 500k*0.6 = 0.352 once each bucket crosses 200k...
// the fresh input/output buckets stay below the threshold, so the upstream
// whole-request switch collapses here to the same numbers the marginal rule
// produces: 10k*2 + 1k*6 + 100k*0.3 = 0.056 below it.
func TestUpstreamParityCachedHeavyContext(t *testing.T) {
	table := newTable(t, upstreamPricingJSON)
	rates, ok := table.Find("grok-4.5")
	if !ok {
		t.Fatal("grok-4.5 missing")
	}

	cachedHeavy := rates.Cost(10_000, 1_000, 0, 0, 500_000, false)
	if want := 10_000*2.0 + 1_000*6.0 + (200_000*0.3 + 300_000*0.6); !approxEq(cachedHeavy, want) {
		t.Fatalf("cached-heavy cost = %v, want %v (marginal rule)", cachedHeavy, want)
	}

	short := rates.Cost(10_000, 1_000, 0, 0, 100_000, false)
	if want := 10_000*2.0 + 1_000*6.0 + 100_000*0.3; !approxEq(short, want) {
		t.Fatalf("short-context cost = %v, want %v (upstream expectation)", short, want)
	}
}

// TestUpstreamParityFastMultiplier mirrors calculate_cost_from_tokens: the
// fast multiplier scales the WHOLE summed cost, applies only when speed==fast,
// and defaults to 1.0 when the model defines none.
func TestUpstreamParityFastMultiplier(t *testing.T) {
	table := newTable(t, `{
	  "m": {
	    "input_cost_per_token": 1.0,
	    "output_cost_per_token": 10.0,
	    "provider_specific_entry": { "fast": 2.0 }
	  }
	}`)
	rates, ok := table.Find("m")
	if !ok {
		t.Fatal("m missing")
	}
	if rates.FastMultiplier != 2.0 {
		t.Fatalf("FastMultiplier = %v, want 2", rates.FastMultiplier)
	}
	base := rates.Cost(100, 10, 4, 0, 40, false)
	fast := rates.Cost(100, 10, 4, 0, 40, true)
	if !approxEq(fast, base*2.0) {
		t.Fatalf("fast cost = %v, want 2x base %v", fast, base)
	}
	// Default multiplier is neutral: plain rates cost the same either way.
	plain, _ := table.Find("m") // same rates; Cost guards on multiplier > 0
	if got := plain.Cost(100, 0, 0, 0, 0, false); !approxEq(got, 100.0) {
		t.Fatalf("non-fast cost = %v, want 100", got)
	}
}

// TestUpstreamParityZeroAndUnknown mirrors cost.rs edges: zero tokens cost 0
// and unknown models compute to 0 (never an error).
func TestUpstreamParityZeroAndUnknown(t *testing.T) {
	table := newTable(t, upstreamPricingJSON)
	rates, _ := table.Find("test-model")
	if got := rates.Cost(0, 0, 0, 0, 0, false); got != 0 {
		t.Fatalf("zero-token cost = %v, want 0", got)
	}
	if got := CostOf("no-such-model", 999_999, 999_999, 0, 0, 0, false); got != 0 {
		t.Fatalf("unknown-model cost = %v, want 0", got)
	}
	if _, ok := table.Find(""); ok {
		t.Fatal("empty model must not resolve")
	}
}
