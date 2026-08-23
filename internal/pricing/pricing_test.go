package pricing

import (
	"math"
	"testing"
)

func approxEq(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// setAliases points XUSAGE_MODEL_ALIASES at raw for the test duration.
func setAliases(t *testing.T, raw string) {
	t.Helper()
	t.Setenv("XUSAGE_MODEL_ALIASES", raw)
	resetAliasCache()
	t.Cleanup(func() { resetAliasCache() })
}

func TestFindExact(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
		in    float64 // expected input rate; ignored when want=false
	}{
		{"exact snapshot key", "claude-sonnet-4-20250514", true, 3e-06},
		{"exact non-dated key", "claude-sonnet-4-5", true, 3e-06},
		{"gpt-5.2 exists", "gpt-5.2", true, 1.75e-06},
		{"case-only mismatch", "Claude-Sonnet-4-5", true, 3e-06},
		{"unknown model", "totally-fake-model-xyz", false, 0},
		{"empty model", "", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rates, ok := Get().Find(tt.model)
			if ok != tt.want {
				t.Fatalf("Find(%q) ok = %v, want %v", tt.model, ok, tt.want)
			}
			if !ok {
				return
			}
			if !approxEq(rates.Input, tt.in) {
				t.Fatalf("Find(%q).Input = %v, want %v", tt.model, rates.Input, tt.in)
			}
		})
	}
}

func TestFindNormalized(t *testing.T) {
	tests := []struct {
		model string
		want  string // snapshot key whose rates must come back
	}{
		{"us.writer.palmyra-x4-v1:0", "us.writer.palmyra-x4-v1:0"},
		{"US.Writer.Palmyra-X4-V1:0", "us.writer.palmyra-x4-v1:0"},
		{"amazon.nova-lite-v1:0", "amazon.nova-lite-v1:0"},
		{"gemini-2.5-pro", "gemini-2.5-pro"},
	}
	for _, tt := range tests {
		rates, ok := Get().Find(tt.model)
		if !ok {
			t.Errorf("Find(%q) = not found, want rates", tt.model)
			continue
		}
		base, _ := Get().Find(tt.want)
		if !approxEq(rates.Input, base.Input) || !approxEq(rates.Output, base.Output) {
			t.Errorf("Find(%q) = %+v, want rates of %q (%+v)", tt.model, rates, tt.want, base)
		}
	}
}

func TestFindDateSuffixStripped(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		target string
	}{
		{
			name:   "dash-separated date suffix",
			query:  "claude-sonnet-4-5-20991231",
			target: "claude-sonnet-4-5",
		},
		{
			name:   "dotted key with date suffix normalizes then strips",
			query:  "bedrock.anthropic.claude-sonnet-4-5.20250929",
			target: "bedrock/us-gov-east-1/claude-sonnet-4-5-20250929-v1:0",
		},
		{
			name:   "date suffix with colon separator",
			query:  "gemini-2.5-pro:20260101",
			target: "gemini-2.5-pro",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Get().Find(tt.query)
			if !ok {
				t.Fatalf("Find(%q) = not found, want it to resolve via %q", tt.query, tt.target)
			}
			base, ok := Get().Find(tt.target)
			if !ok {
				t.Fatalf("target %q missing from snapshot", tt.target)
			}
			if !approxEq(got.Input, base.Input) || !approxEq(got.Output, base.Output) {
				t.Fatalf("Find(%q) = %+v, want %+v", tt.query, got, base)
			}
		})
	}
}

func TestFindPrefixFallback(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		target string
	}{
		{
			name:   "single trailing segment",
			query:  "claude-sonnet-4-5-preview",
			target: "claude-sonnet-4-5",
		},
		{
			name:   "provider prefix walks down to bare model",
			query:  "azure/eu/gpt-5.1-turbo-x",
			target: "azure/eu/gpt-5.1",
		},
		{
			name:   "dotted bedrock spelling falls to dash-spelled model",
			query:  "bedrock.anthropic.claude-opus-4-1-20250805-v1:0",
			target: "claude-opus-4-1-20250805",
		},
		{
			name:   "unknown family never matches",
			query:  "no-such-family-xyz/variant",
			target: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Get().Find(tt.query)
			if tt.target == "" {
				if ok {
					t.Fatalf("Find(%q) unexpectedly resolved to %+v", tt.query, got)
				}
				return
			}
			if !ok {
				t.Fatalf("Find(%q) = not found, want resolve via %q", tt.query, tt.target)
			}
			base, _ := Get().Find(tt.target)
			if !approxEq(got.Input, base.Input) || !approxEq(got.Output, base.Output) {
				t.Fatalf("Find(%q) = %+v, want %+v", tt.query, got, base)
			}
		})
	}
}

func TestFindAliasResolution(t *testing.T) {
	tests := []struct {
		name   string
		raw    string // XUSAGE_MODEL_ALIASES payload
		query  string
		target string // snapshot key whose rates must come back; "" = not found
	}{
		{
			name:   "json object alias",
			raw:    `{"my-alias":"claude-sonnet-4-5"}`,
			query:  "my-alias",
			target: "claude-sonnet-4-5",
		},
		{
			name:   "list form alias",
			raw:    "corp-model=gpt-5.2, other=gemini-2.5-pro",
			query:  "corp-model",
			target: "gpt-5.2",
		},
		{
			name:   "fast suffix inherits base alias",
			raw:    "corp-model=gpt-5.2",
			query:  "corp-model-fast",
			target: "gpt-5.2",
		},
		{
			name:   "fast alias with explicit fast target",
			raw:    "corp-model=claude-sonnet-4-5",
			query:  "corp-model-fast",
			target: "claude-sonnet-4-5",
		},
		{
			name:   "alias target itself resolves through fallbacks",
			raw:    "legacy=claude-sonnet-4-20250514",
			query:  "legacy",
			target: "claude-sonnet-4-20250514",
		},
		{
			name:   "invalid json ignored",
			raw:    "{not json",
			query:  "anything",
			target: "",
		},
		{
			name:   "empty alias value ignored",
			raw:    "ghost=",
			query:  "ghost",
			target: "",
		},
		{
			name:   "no alias set",
			raw:    "",
			query:  "claude-sonnet-4-5",
			target: "claude-sonnet-4-5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAliases(t, tt.raw)
			rates, ok := Get().Find(tt.query)
			if tt.target == "" {
				if ok {
					t.Fatalf("Find(%q) unexpectedly resolved to %+v", tt.query, rates)
				}
				return
			}
			if !ok {
				t.Fatalf("Find(%q) = not found, want alias to %q", tt.query, tt.target)
			}
			base, _ := Get().Find(tt.target)
			if !approxEq(rates.Input, base.Input) || !approxEq(rates.Output, base.Output) {
				t.Fatalf("Find(%q) = %+v, want rates of %q (%+v)", tt.query, rates, tt.target, base)
			}
		})
	}
}

// TestFindAliasCacheTracksEnvChange pins the change-aware cache: a second
// lookup after the env changed must observe the new value.
func TestFindAliasCacheTracksEnvChange(t *testing.T) {
	setAliases(t, "m1=gpt-5.2")
	if _, ok := Get().Find("m1"); !ok {
		t.Fatal("first alias lookup missed")
	}
	setAliases(t, "m2=gpt-5.2")
	if _, ok := Get().Find("m2"); !ok {
		t.Fatal("alias change after first lookup was not observed")
	}
	if _, ok := Get().Find("m1"); ok {
		t.Fatal("stale alias survived env change")
	}
}

// newTable builds a table from LiteLLM-style JSON for Cost math tests.
func newTable(t *testing.T, litellm string) *Table {
	t.Helper()
	if err := SetTable([]byte(litellm)); err != nil {
		t.Fatalf("SetTable: %v", err)
	}
	t.Cleanup(func() { SetTable(nil) }) // restore embedded snapshot
	return Get()
}

const testPricingJSON = `{
  "test-model": {
    "input_cost_per_token": 1.0,
    "output_cost_per_token": 10.0,
    "cache_creation_input_token_cost": 1.25,
    "cache_read_input_token_cost": 0.1,
    "input_cost_per_token_above_200k_tokens": 2.0,
    "output_cost_per_token_above_200k_tokens": 20.0,
    "cache_creation_input_token_cost_above_200k_tokens": 2.5,
    "cache_read_input_token_cost_above_200k_tokens": 0.2
  },
  "plain-model": {
    "input_cost_per_token": 1.0,
    "output_cost_per_token": 10.0
  },
  "fast-model": {
    "input_cost_per_token": 1.0,
    "output_cost_per_token": 10.0,
    "provider_specific_entry": { "fast": 2.0 }
  },
  "fast2-model": {
    "input_cost_per_token": 1.0,
    "output_cost_per_token": 10.0,
    "fast_multiplier": 3.0
  }
}`

func TestRatesCost(t *testing.T) {
	table := newTable(t, testPricingJSON)
	rates, ok := table.Find("test-model")
	if !ok {
		t.Fatal("test-model missing")
	}
	plain, ok := table.Find("plain-model")
	if !ok {
		t.Fatal("plain-model missing")
	}

	tests := []struct {
		name  string
		rates Rates
		in    uint64
		out   uint64
		cc5   uint64
		cc1h  uint64
		cr    uint64
		fast  bool
		want  float64
	}{
		{
			name: "zero tokens cost zero",
			want: 0,
		},
		{
			name: "plain input and output",
			in:   100, out: 10,
			want: 100*1.0 + 10*10.0,
		},
		{
			name: "cache creation 5m uses explicit rate",
			cc5:  4,
			want: 4 * 1.25,
		},
		{
			name: "cache creation 1h uses double input rate",
			cc1h: 3,
			want: 3 * 2.0,
		},
		{
			name: "cache read uses explicit rate",
			cr:   40,
			want: 40 * 0.1,
		},
		{
			name: "marginal 200k split on input",
			in:   200_100,
			want: 200_000*1.0 + 100*2.0,
		},
		{
			name: "exactly at threshold stays on base rate",
			in:   200_000,
			want: 200_000 * 1.0,
		},
		{
			name: "marginal split applies per bucket",
			in:   300_000, out: 200_050,
			want: (200_000*1.0 + 100_000*2.0) + (200_000*10.0 + 50*20.0),
		},
		{
			name: "no above rate means no tiering",
			rates: Rates{
				Input: 1.0, Output: 10.0,
				CacheCreate: 1.25, CacheRead: 0.1,
				// no *Above200k set
			},
			in:   500_000,
			want: 500_000 * 1.0,
		},
		{
			name: "fast multiplier scales the whole cost",
			in:   100,
			want: 100 * 1.0,
		},
		{
			name: "long context with every bucket",
			rates: Rates{
				Input: 1.0, Output: 10.0,
				CacheCreate: 1.25, CacheRead: 0.1,
				InputAbove200k: 2.0, OutputAbove200k: 20.0,
				CacheCreateAbove200k: 2.5, CacheReadAbove200k: 0.2,
			},
			in: 250_000, out: 1_000, cc5: 200_500, cc1h: 1_000, cr: 300_000,
			// Only buckets exceeding 200k split marginally: input and cache
			// read do; output and cc1h stay whole at their base rates.
			want: (200_000*1.0 + 50_000*2.0) +
				1_000*10.0 +
				(200_000*1.25 + 500*2.5) +
				1_000*(2.0) +
				(200_000*0.1 + 100_000*0.2),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.rates
			if r == (Rates{}) {
				r = rates
			}
			got := r.Cost(tt.in, tt.out, tt.cc5, tt.cc1h, tt.cr, tt.fast)
			if !approxEq(got, tt.want) {
				t.Fatalf("Cost() = %v, want %v", got, tt.want)
			}
		})
	}

	// Fast speed: multiplier 1.0 by default, table value when present.
	// fast-model's multiplier arrives through the LiteLLM
	// provider_specific_entry.fast field parsed in loadInto.
	fm, _ := table.Find("fast-model")
	if got := rates.Cost(100, 0, 0, 0, 0, true); !approxEq(got, 100*1.0) {
		t.Errorf("fast on default multiplier = %v, want %v", got, 100*1.0)
	}
	if got := plain.Cost(100, 0, 0, 0, 0, true); !approxEq(got, 100*1.0) {
		t.Errorf("fast on plain rates = %v, want %v", got, 100*1.0)
	}
	if fm.FastMultiplier != 2.0 {
		t.Errorf("fast-model FastMultiplier = %v, want 2 (from provider_specific_entry)", fm.FastMultiplier)
	}
	if got := fm.Cost(100, 0, 0, 0, 0, true); !approxEq(got, 200.0) {
		t.Errorf("fast with table multiplier = %v, want 200", got)
	}
	if got := fm.Cost(100, 0, 0, 0, 0, false); !approxEq(got, 100.0) {
		t.Errorf("non-fast ignores multiplier = %v, want 100", got)
	}

	// Top-level fast_multiplier wins over provider_specific_entry.fast.
	fm2, ok := table.Find("fast2-model")
	if !ok {
		t.Fatal("fast2-model missing")
	}
	if fm2.FastMultiplier != 3.0 {
		t.Errorf("fast2-model FastMultiplier = %v, want 3 (top-level fast_multiplier)", fm2.FastMultiplier)
	}
}

func TestCostOf(t *testing.T) {
	table := newTable(t, testPricingJSON)

	tests := []struct {
		name  string
		model string
		in    uint64
		want  float64
	}{
		{"known model computed", "test-model", 10, 10.0},
		{"unknown model is zero", "nope-model", 1_000_000, 0},
		{"empty model is zero", "", 5, 0},
		{"zero tokens is zero", "test-model", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CostOf(tt.model, tt.in, 0, 0, 0, 0, false); !approxEq(got, tt.want) {
				t.Fatalf("CostOf(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
	_ = table
}

func TestKnown(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-20250514", true},
		{"claude-sonnet-4-5-fast", true},
		{"not-a-model", false},
	}
	for _, tt := range tests {
		if got := Get().Known(tt.model); got != tt.want {
			t.Errorf("Known(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

// TestEmbeddedSnapshotParses guards the embedded snapshot: it must load, and
// models the dashboard actually sees must resolve.
func TestEmbeddedSnapshotParses(t *testing.T) {
	SetTable(nil) // restore embedded snapshot, discarding any override
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-20250514", true},
		{"claude-sonnet-4-20250514-fast", true},
		{"gpt-5.2", true},
		{"gpt-5.2-codex", true},
	}
	for _, tt := range tests {
		rates, ok := Get().Find(tt.model)
		if ok != tt.want {
			t.Errorf("Find(%q) ok = %v, want %v", tt.model, ok, tt.want)
			continue
		}
		if ok && (rates.Input <= 0 || rates.Output <= 0) {
			t.Errorf("Find(%q) has non-positive rates %+v", tt.model, rates)
		}
	}
	if n := len(Get().rates); n < 1000 {
		t.Errorf("embedded snapshot holds %d models, want >= 1000", n)
	}
}

// TestGPT52SanePrices pins gpt-5.2 to plausible magnitudes so a corrupted
// snapshot (string-encoded or zeroed costs) cannot slip through.
func TestGPT52SanePrices(t *testing.T) {
	SetTable(nil)
	r, ok := Get().Find("gpt-5.2")
	if !ok {
		t.Fatal("gpt-5.2 not found in embedded snapshot")
	}
	if r.Input <= 0 || r.Input >= 0.001 {
		t.Errorf("gpt-5.2 input rate %v not in (0, 0.001)", r.Input)
	}
	if r.Output <= r.Input || r.Output >= 0.01 {
		t.Errorf("gpt-5.2 output rate %v not in (input, 0.01)", r.Output)
	}
	cost := r.Cost(1_000_000, 2_000_000, 0, 0, 0, false)
	if want := 1_000_000*r.Input + 2_000_000*r.Output; !approxEq(cost, want) {
		t.Errorf("gpt-5.2 1M-in/2M-out cost = %v, want %v", cost, want)
	}
}

// TestConcurrentFindAndSwap smokes the lock-free contract: readers call Find on
// whatever table is current while another goroutine swaps tables. Under -race
// this is the primary regression net for the atomic.Pointer handoff.
func TestConcurrentFindAndSwap(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			_, _ = Get().Find("test-model")
			_ = Get().Known("claude-sonnet-4-5")
			_ = CostOf("test-model", 10, 10, 0, 0, 0, false)
		}
	}()
	for i := 0; i < 100; i++ {
		if err := SetTable([]byte(testPricingJSON)); err != nil {
			t.Fatalf("SetTable: %v", err)
		}
		if err := SetTable(nil); err != nil {
			t.Fatalf("SetTable(nil): %v", err)
		}
	}
	<-done
}
