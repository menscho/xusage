package pricing

import (
	"testing"
)

// TestUpstreamCostParity pins Rates.Cost against the test vectors from
// upstream ccusage-core cost.rs (its own #[test] cases), so the Go math
// cannot drift from the Rust reference:
// https://github.com/ccusage/ccusage/blob/main/rust/crates/ccusage-core/src/cost.rs
func TestUpstreamCostParity(t *testing.T) {
	table := newTable(t, `{
	  "test-model": {
	    "input_cost_per_token": 1.0,
	    "output_cost_per_token": 10.0,
	    "cache_creation_input_token_cost": 1.25,
	    "cache_read_input_token_cost": 0.1,
	    "input_cost_per_token_above_200k_tokens": 2.0,
	    "cache_creation_input_token_cost_above_200k_tokens": 1.5
	  }
	}`)
	rates, ok := table.Find("test-model")
	if !ok {
		t.Fatal("test-model missing")
	}

	tests := []struct {
		name string
		in   uint64
		out  uint64
		cc5  uint64 // flat / ephemeral_5m bucket
		cc1h uint64 // ephemeral_1h bucket
		cr   uint64
		want float64
	}{
		{
			// upstream prices_cache_creation_breakdown_by_duration:
			// 10*1.25 + 20*(2*1.0) + 30*0.1 = 55.5
			name: "breakdown priced by duration",
			cc5:  10, cc1h: 20, cr: 30,
			want: 55.5,
		},
		{
			// upstream falls_back_to_flat_cache_creation_rate_without_breakdown:
			// 10*1.25 = 12.5
			name: "flat cache creation fallback",
			cc5:  10,
			want: 12.5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rates.Cost(tt.in, tt.out, tt.cc5, tt.cc1h, tt.cr, false)
			if !approxEq(got, tt.want) {
				t.Fatalf("Cost() = %v, want %v (upstream cost.rs vector)", got, tt.want)
			}
		})
	}

	// Marginal 200k split (tiered_cost): 200_100 input tokens with a 2.0
	// above-rate bills 200_000 at base + 100 at above.
	got := rates.Cost(200_100, 0, 0, 0, 0, false)
	if want := 200_000*1.0 + 100*2.0; !approxEq(got, want) {
		t.Errorf("marginal tier Cost() = %v, want %v", got, want)
	}
}
