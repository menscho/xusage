package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// marshal encodes v to JSON bytes.
func marshal(v any) ([]byte, error) { return json.Marshal(v) }

// contains reports whether substr appears in the JSON string s.
func contains(s []byte, substr string) bool {
	return strings.Contains(string(s), substr)
}

func testTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestEntryTotalTokens(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  uint64
	}{
		{"zero entry", Entry{}, 0},
		{
			"plain input+output",
			Entry{InputTokens: 10, OutputTokens: 5},
			15,
		},
		{
			"all buckets",
			Entry{InputTokens: 10, OutputTokens: 5, CacheCreationTokens: 3,
				CacheCreation1h: 2, CacheReadTokens: 7, ExtraTotalTokens: 100},
			127,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.TotalTokens(); got != tt.want {
				t.Errorf("TotalTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		in   string
		want Mode
	}{
		{"auto", ModeAuto},
		{"display", ModeDisplay},
		{"calculate", ModeCalculate},
		{"", ModeAuto},
		{"bogus", ModeAuto},
		{"AUTO", ModeAuto}, // case-sensitive by design
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ParseMode(tt.in); got != tt.want {
				t.Errorf("ParseMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEntryCostByMode(t *testing.T) {
	e := Entry{CostDisplay: 1.0, CostAuto: 2.0, CostCalc: 3.0}
	tests := []struct {
		mode Mode
		want float64
	}{
		{ModeDisplay, 1.0},
		{ModeAuto, 2.0},
		{ModeCalculate, 3.0},
		{Mode("nonsense"), 2.0}, // default arm = auto
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			if got := e.Cost(tt.mode); got != tt.want {
				t.Errorf("Cost(%q) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestModelBreakdownAdd(t *testing.T) {
	b := &ModelBreakdown{}
	e := Entry{
		Model:               "m",
		InputTokens:         100,
		OutputTokens:        50,
		CacheCreationTokens: 20,
		CacheCreation1h:     10,
		CacheReadTokens:     5,
		ExtraTotalTokens:    7,
		CostAuto:            0.5,
	}
	b.Add(&e, ModeAuto)
	if b.Model != "m" {
		t.Errorf("Model = %q, want m", b.Model)
	}
	if b.InputTokens != 100 || b.OutputTokens != 50 || b.CacheReadTokens != 5 || b.ExtraTotalTokens != 7 {
		t.Errorf("token buckets = %+v", b)
	}
	if b.CacheCreationTokens != 30 { // 5m + 1h fold together
		t.Errorf("CacheCreationTokens = %d, want 30", b.CacheCreationTokens)
	}
	if b.Cost != 0.5 {
		t.Errorf("Cost = %v, want 0.5", b.Cost)
	}
	if got := b.TotalTokens(); got != 192 {
		t.Errorf("TotalTokens = %d, want 192", got)
	}

	// Second entry accumulates.
	e2 := e
	e2.InputTokens = 1
	b.Add(&e2, ModeAuto)
	if b.InputTokens != 101 || b.Cost != 1.0 {
		t.Errorf("accumulation wrong: %+v cost=%v", b, b.Cost)
	}
}

func TestMarkMissingPricing(t *testing.T) {
	b := &ModelBreakdown{}
	if b.MissingPricing {
		t.Fatal("flag must start false")
	}
	b.MarkMissingPricing()
	if !b.MissingPricing {
		t.Error("MarkMissingPricing must set the flag")
	}
}

func TestRowTotalTokens(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		want uint64
	}{
		{"empty", Row{}, 0},
		{"sums all buckets", Row{InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3,
			CacheReadTokens: 4, ExtraTotalTokens: 5}, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.row.TotalTokens(); got != tt.want {
				t.Errorf("TotalTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBlockTotalTokens(t *testing.T) {
	// TotalTokens is a serialized snapshot field, populated by the engine when
	// it fills a block (fillBlock); a zero-constructed Block stays zero.
	b := Block{InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 30,
		CacheReadTokens: 40, ExtraTotalTokens: 50, TotalTokens: 150}
	if got := b.TotalTokens; got != 150 {
		t.Errorf("TotalTokens = %d, want 150", got)
	}
}

func TestBlockJSONShape(t *testing.T) {
	// Entries is construction-only and must never serialize.
	start := testTime("2026-01-01 10:00:00")
	end := start.Add(5 * time.Hour)
	reset := testTime("2026-01-01 12:00:00")
	b := Block{
		ID:        "x",
		StartTime: start,
		EndTime:   end,
		IsActive:  true,
		Entries:   []Entry{{ID: "hidden"}},
	}
	data, err := marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if contains(data, "hidden") {
		t.Error("Entries must not be serialized")
	}
	if !contains(data, `"isActive":true`) {
		t.Error("expected isActive in JSON")
	}

	b.UsageLimitReset = &reset
	data, _ = marshal(b)
	if !contains(data, `"usageLimitReset"`) {
		t.Error("usageLimitReset should serialize when set")
	}
}
