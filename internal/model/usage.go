// Package model defines the shared usage data types passed between sources,
// the aggregation engine, and the HTTP API.
package model

import "time"

// Mode selects how costs are resolved for reporting.
type Mode string

const (
	// ModeAuto uses the provider-reported cost when present, otherwise computes
	// from token counts and the pricing table.
	ModeAuto Mode = "auto"
	// ModeDisplay always uses the provider-reported cost (0 when absent).
	ModeDisplay Mode = "display"
	// ModeCalculate always computes from token counts and the pricing table.
	ModeCalculate Mode = "calculate"
)

// ParseMode normalizes a raw mode string, defaulting to ModeAuto.
func ParseMode(s string) Mode {
	switch Mode(s) {
	case ModeDisplay, ModeCalculate:
		return Mode(s)
	default:
		return ModeAuto
	}
}

// Entry is a single normalized usage event from any supported source.
// Adapters produce Entries; the engine deduplicates, prices, and aggregates them.
type Entry struct {
	ID        string    `json:"id"`
	RequestID string    `json:"requestId,omitempty"`
	Timestamp time.Time `json:"timestamp"`

	Model     string `json:"model"`              // display model (may carry a "-fast" suffix)
	RawModel  string `json:"rawModel,omitempty"` // model exactly as logged
	Provider  string `json:"provider"`           // source name: claude, codex, opencode, ...
	Project   string `json:"project,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Version   string `json:"version,omitempty"`

	InputTokens         uint64 `json:"inputTokens"`
	OutputTokens        uint64 `json:"outputTokens"`
	CacheCreationTokens uint64 `json:"cacheCreationTokens"` // ephemeral 5m, or undifferentiated
	CacheCreation1h     uint64 `json:"cacheCreation1h,omitempty"`
	CacheReadTokens     uint64 `json:"cacheReadTokens"`
	ExtraTotalTokens    uint64 `json:"extraTotalTokens,omitempty"` // provider reported only a grand total

	// Costs are resolved once at load time so requests never re-price.
	CostDisplay float64 `json:"-"` // reported cost, or 0
	CostAuto    float64 `json:"-"` // reported cost when present, else computed
	CostCalc    float64 `json:"-"` // always computed

	ReportedCost *float64 `json:"-"` // raw costUSD-style value, nil when absent
	SpeedFast    bool     `json:"-"` // usage.speed == "fast"

	Messages        int        `json:"-"` // message count contributed (usually 1)
	IsSidechain     bool       `json:"-"`
	IsAPIError      bool       `json:"-"`
	MissingPricing  bool       `json:"-"` // model had no rates and tokens > 0
	UsageLimitReset *time.Time `json:"-"` // usage-limit-reached reset timestamp

	// SessionIdx interns SessionID to a dense 1-based index, assigned by the
	// engine when a snapshot is constructed (loadAll / NewSnapshotForTest).
	// 0 means "not interned": aggregators may only use it for distinct-session
	// accounting on engine-built snapshots, where the invariant always holds.
	SessionIdx int `json:"-"`
}

// TotalTokens is the full token footprint used for dedup preference and totals.
func (e *Entry) TotalTokens() uint64 {
	return e.InputTokens + e.OutputTokens + e.CacheCreationTokens +
		e.CacheCreation1h + e.CacheReadTokens + e.ExtraTotalTokens
}

// Cost returns the entry cost under the given mode.
func (e *Entry) Cost(m Mode) float64 {
	switch m {
	case ModeDisplay:
		return e.CostDisplay
	case ModeCalculate:
		return e.CostCalc
	default:
		return e.CostAuto
	}
}

// ModelBreakdown aggregates one model's share inside a Row.
type ModelBreakdown struct {
	Model               string  `json:"model"`
	InputTokens         uint64  `json:"inputTokens"`
	OutputTokens        uint64  `json:"outputTokens"`
	CacheCreationTokens uint64  `json:"cacheCreationTokens"`
	CacheReadTokens     uint64  `json:"cacheReadTokens"`
	ExtraTotalTokens    uint64  `json:"extraTotalTokens,omitempty"`
	Cost                float64 `json:"cost"`
	MissingPricing      bool    `json:"missingPricing,omitempty"`
}

// Add accumulates one entry into the breakdown.
func (b *ModelBreakdown) Add(e *Entry, mode Mode) {
	b.Model = e.Model
	b.InputTokens += e.InputTokens
	b.OutputTokens += e.OutputTokens
	b.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
	b.CacheReadTokens += e.CacheReadTokens
	b.ExtraTotalTokens += e.ExtraTotalTokens
	b.Cost += e.Cost(mode)
}

// MarkMissingPricing flags the breakdown when its model had no known price.
// The engine calls this after pricing; adapters never do.
func (b *ModelBreakdown) MarkMissingPricing() {
	b.MissingPricing = true
}

// TotalTokens sums every token bucket of the breakdown.
func (b *ModelBreakdown) TotalTokens() uint64 {
	return b.InputTokens + b.OutputTokens + b.CacheCreationTokens +
		b.CacheReadTokens + b.ExtraTotalTokens
}

// Row is a generic aggregate row: a period, a session, a project, or a model.
type Row struct {
	Key                 string           `json:"key"`             // date / week / month / hour / id
	Label               string           `json:"label,omitempty"` // display label when it differs from Key
	InputTokens         uint64           `json:"inputTokens"`
	OutputTokens        uint64           `json:"outputTokens"`
	CacheCreationTokens uint64           `json:"cacheCreationTokens"`
	CacheReadTokens     uint64           `json:"cacheReadTokens"`
	ExtraTotalTokens    uint64           `json:"extraTotalTokens,omitempty"`
	Cost                float64          `json:"cost"`
	Messages            int64            `json:"messages,omitempty"`
	Sessions            int64            `json:"sessions,omitempty"`
	Models              []ModelBreakdown `json:"models,omitempty"`
	FirstActivity       time.Time        `json:"firstActivity,omitempty"`
	LastActivity        time.Time        `json:"lastActivity,omitempty"`
}

// TotalTokens sums every token bucket of the row.
func (r *Row) TotalTokens() uint64 {
	return r.InputTokens + r.OutputTokens + r.CacheCreationTokens +
		r.CacheReadTokens + r.ExtraTotalTokens
}

// BurnRate describes consumption velocity of an active block.
type BurnRate struct {
	TokensPerMinute float64 `json:"tokensPerMinute"`
	CostPerHour     float64 `json:"costPerHour"`
}

// Projection extrapolates an active block to its end.
type Projection struct {
	TotalTokens      uint64  `json:"totalTokens"`
	TotalCost        float64 `json:"totalCost"`
	RemainingMinutes int64   `json:"remainingMinutes"`
}

// Block is a billing window (default 5h), a gap between windows, or the live window.
type Block struct {
	ID                  string      `json:"id"`
	StartTime           time.Time   `json:"startTime"`
	EndTime             time.Time   `json:"endTime"`
	ActualEndTime       *time.Time  `json:"actualEndTime,omitempty"`
	IsActive            bool        `json:"isActive"`
	IsGap               bool        `json:"isGap"`
	InputTokens         uint64      `json:"inputTokens"`
	OutputTokens        uint64      `json:"outputTokens"`
	CacheCreationTokens uint64      `json:"cacheCreationTokens"`
	CacheReadTokens     uint64      `json:"cacheReadTokens"`
	ExtraTotalTokens    uint64      `json:"extraTotalTokens,omitempty"`
	TotalTokens         uint64      `json:"totalTokens"`
	Cost                float64     `json:"cost"`
	Messages            int64       `json:"messages,omitempty"`
	Models              []string    `json:"models,omitempty"`
	BurnRate            *BurnRate   `json:"burnRate,omitempty"`
	Projection          *Projection `json:"projection,omitempty"`
	UsageLimitReset     *time.Time  `json:"usageLimitReset,omitempty"`

	// Entries holds this block's entries as an owned value copy (never a
	// snapshot alias). It is read by the engine's fillBlock for token/cost
	// aggregation and by blockBurnRate for the first/last timestamps, so it is
	// NOT vestigial even though the API never serializes it (json:"-"). Removing
	// it requires reworking internal/engine/blocks.go and breaks its tests.
	Entries []Entry `json:"-"`
}
