// Package engine — 5-hour billing window (block) computation.
//
// Port of ccusage's identify_session_blocks: a block starts at the floor-to-
// hour of its first entry; a new block opens when an entry arrives more than
// sessionDuration after the block start OR more than sessionDuration after the
// previous entry. The latter case inserts an explicit gap block covering
// [last+sessionDuration, next entry) so the UI can render dead time honestly.
// Block IDs mirror upstream's RFC3339-millis UTC start ("gap-" prefixed for
// gaps); burn rate spreads tokens/cost over the exact first-to-last entry
// span (no rate at all for a zero span).
package engine

import (
	"math"
	"time"

	"xusage/internal/model"
)

// DefaultBlockHours is Claude's billing window length in hours.
const DefaultBlockHours = 5

// Blocks computes billing windows over filtered entries.
// now is used to decide which block is active; pass time.Now() in production.
func (s *Snapshot) Blocks(o *Opts, sessionDurationHours float64, now time.Time) []model.Block {
	// The snapshot is already sorted by timestamp (loadAll sorts after dedup),
	// so Blocks works on the filtered view directly — no per-request sort.
	entries := s.filtered(o)

	sessionDuration := time.Duration(sessionDurationHours * float64(time.Hour))
	blocks := identifyBlocks(entries, sessionDuration, now)
	for i := range blocks {
		fillBlock(&blocks[i], o.Mode, now)
	}
	return blocks
}

// ActiveBlock returns the currently active block, or nil.
func (s *Snapshot) ActiveBlock(o *Opts, now time.Time) *model.Block {
	blocks := s.Blocks(o, DefaultBlockHours, now)
	for i := range blocks {
		if blocks[i].IsActive {
			return &blocks[i]
		}
	}
	return nil
}

// floorToHour truncates a timestamp down to the top of its hour. Hour
// boundaries are absolute time, so the result is normalized to UTC.
func floorToHour(t time.Time) time.Time {
	return t.Truncate(time.Hour).UTC()
}

// rfc3339Millis formats a block ID the way upstream's format_rfc3339_millis
// does: UTC with exactly three fractional-second digits.
func rfc3339Millis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// identifyBlocks splits timestamp-sorted entries into billing windows with
// upstream semantics: the open block closes when an entry is more than
// sessionDuration after the block start or after the previous entry; in the
// latter case a gap block spans [last+sessionDuration, next entry).
func identifyBlocks(entries []*model.Entry, sessionDuration time.Duration, now time.Time) []model.Block {
	if len(entries) == 0 {
		return nil
	}
	var blocks []model.Block
	var currentStart time.Time
	var current []*model.Entry

	closeBlock := func() {
		end := currentStart.Add(sessionDuration)
		actual := current[len(current)-1].Timestamp
		isActive := end.After(now) && now.Sub(actual) < sessionDuration
		blocks = append(blocks, model.Block{
			ID:            rfc3339Millis(currentStart),
			StartTime:     currentStart,
			EndTime:       end,
			ActualEndTime: &actual,
			IsActive:      isActive,
			Entries:       derefEntries(current),
		})
		current = nil // fresh backing array: closed blocks keep their entries
	}

	for _, e := range entries {
		if !currentStart.IsZero() {
			last := current[len(current)-1].Timestamp
			sinceStart := e.Timestamp.Sub(currentStart)
			sinceLast := e.Timestamp.Sub(last)
			if sinceStart > sessionDuration || sinceLast > sessionDuration {
				closeBlock()
				if sinceLast > sessionDuration {
					gapStart := last.Add(sessionDuration)
					blocks = append(blocks, model.Block{
						ID:        "gap-" + rfc3339Millis(gapStart),
						StartTime: gapStart,
						EndTime:   e.Timestamp,
						IsGap:     true,
					})
				}
				currentStart = time.Time{}
			}
		}
		if currentStart.IsZero() {
			currentStart = floorToHour(e.Timestamp)
		}
		current = append(current, e)
	}
	if len(current) > 0 {
		closeBlock()
	}
	return blocks
}

// derefEntries copies pointer-referenced entries into an owned value slice:
// Block.Entries backs the API response and must not alias the snapshot.
func derefEntries(ptrs []*model.Entry) []model.Entry {
	out := make([]model.Entry, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out
}

// fillBlock aggregates tokens/costs and derives burn rate + projection for
// the active block. Gap blocks stay empty by definition. Models are listed in
// first-seen order and usageLimitReset takes the first reset seen, both
// mirroring upstream's entry scan.
func fillBlock(b *model.Block, mode model.Mode, now time.Time) {
	if b.IsGap {
		return
	}
	modelsSeen := map[string]bool{}
	for i := range b.Entries {
		e := &b.Entries[i]
		b.InputTokens += e.InputTokens
		b.OutputTokens += e.OutputTokens
		b.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
		b.CacheReadTokens += e.CacheReadTokens
		b.ExtraTotalTokens += e.ExtraTotalTokens
		b.Cost += e.Cost(mode)
		b.Messages += int64(max(e.Messages, 1))
		if e.Model != "" && !modelsSeen[e.Model] {
			modelsSeen[e.Model] = true
			b.Models = append(b.Models, e.Model)
		}
		if b.UsageLimitReset == nil && e.UsageLimitReset != nil {
			t := *e.UsageLimitReset
			b.UsageLimitReset = &t
		}
	}
	b.TotalTokens = b.InputTokens + b.OutputTokens + b.CacheCreationTokens +
		b.CacheReadTokens + b.ExtraTotalTokens

	if b.IsActive {
		if br := blockBurnRate(b); br != nil {
			b.BurnRate = br
			b.Projection = projectBlock(b, br, now)
		}
	}
}

// blockBurnRate mirrors upstream calculate_burn_rate: tokens and cost spread
// over the exact elapsed span from first to last entry. A non-positive span
// yields no rate (upstream returns None), so a burst inside a single instant
// produces neither burn rate nor projection rather than an infinite one.
func blockBurnRate(b *model.Block) *model.BurnRate {
	if len(b.Entries) == 0 {
		return nil
	}
	minutes := b.Entries[len(b.Entries)-1].Timestamp.Sub(b.Entries[0].Timestamp).Minutes()
	if minutes <= 0 {
		return nil
	}
	total := float64(b.TotalTokens)
	return &model.BurnRate{
		TokensPerMinute: total / minutes,
		CostPerHour:     b.Cost / minutes * 60,
	}
}

// projectBlock extrapolates the active block to its scheduled end using the
// observed burn rate; remaining minutes are rounded like upstream.
func projectBlock(b *model.Block, br *model.BurnRate, now time.Time) *model.Projection {
	remaining := math.Round(b.EndTime.Sub(now).Minutes())
	if remaining < 0 {
		remaining = 0
	}
	totalTokens := uint64(math.Round(float64(b.TotalTokens) + br.TokensPerMinute*remaining))
	totalCost := math.Round((b.Cost+br.CostPerHour/60*remaining)*100) / 100
	return &model.Projection{
		TotalTokens:      totalTokens,
		TotalCost:        totalCost,
		RemainingMinutes: int64(remaining),
	}
}
