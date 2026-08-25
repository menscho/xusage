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
	// We filter inline (iterating s.Entries through match) instead of calling
	// s.filtered: that helper allocates a []*model.Entry sized to the whole
	// snapshot (8 B/entry), rebuilt on every poll. The aggregators already
	// inline this same loop to dodge that slice; blocks() was the last major
	// caller still paying for it, so this matches the established pattern and
	// removes the allocation from the ActiveBlock hot path.
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	sessionDuration := time.Duration(sessionDurationHours * float64(time.Hour))
	return identifyBlocks(s, o, loc, since, until, sessionDuration, now)
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
//
// It iterates s.Entries inline through match() rather than taking a pre-
// filtered []*model.Entry: that slice (built by Snapshot.filtered) costs 8
// B/entry and is rebuilt on every poll, and the aggregators already avoid it
// the same way. o's TZ/since/until bounds are precomputed by the caller.
//
// Blocks are filled from the pointer slice directly instead of copying entries
// into Block.Entries. The snapshot is immutable during the call, so the
// pointers stay valid for the synchronous fillBlock/blockBurnRate pass that
// completes before the working slice is reused. Block.Entries stays nil (it is
// json:"-" and only those two helpers ever read it), which avoids a full value
// copy of every block's entries on every /api/pulse (~27 MB on a typical
// snapshot).
func identifyBlocks(s *Snapshot, o *Opts, loc *time.Location, since, until time.Time, sessionDuration time.Duration, now time.Time) []model.Block {
	var blocks []model.Block
	var currentStart time.Time
	var current []*model.Entry

	closeBlock := func() {
		end := currentStart.Add(sessionDuration)
		last := current[len(current)-1]
		actual := last.Timestamp
		isActive := end.After(now) && now.Sub(actual) < sessionDuration
		// ActualEndTime aliases the snapshot entry's timestamp instead of
		// copying it into a local and taking its address (which forces the
		// time.Time onto the heap). The snapshot is immutable during the call,
		// so the pointer stays valid for the synchronous JSON marshal that
		// consumes the returned blocks.
		b := model.Block{
			ID:            rfc3339Millis(currentStart),
			StartTime:     currentStart,
			EndTime:       end,
			ActualEndTime: &last.Timestamp,
			IsActive:      isActive,
		}
		fillBlock(&b, current, o.Mode, now)
		blocks = append(blocks, b)
		current = current[:0] // reuse backing array; pointers only read synchronously in fillBlock
	}

	for i := range s.Entries {
		e := &s.Entries[i]
		if !match(o, loc, since, until, e) {
			continue
		}
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

// stringSliceContains reports whether s contains v. Used in fillBlock to dedup
// a block's model list without a per-block map allocation: a 5h window rarely
// holds more than a couple of distinct models, so a linear scan beats a map.
func stringSliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// fillBlock aggregates tokens/costs and derives burn rate + projection for
// the active block. entries is the block's entries as pointers into the
// (immutable) snapshot; they are read synchronously and never stored on the
// block, so Block.Entries stays nil. Gap blocks stay empty by definition.
// Models are listed in first-seen order and usageLimitReset takes the first
// reset seen, both mirroring upstream's entry scan.
func fillBlock(b *model.Block, entries []*model.Entry, mode model.Mode, now time.Time) {
	if b.IsGap {
		return
	}
	for _, e := range entries {
		b.InputTokens += e.InputTokens
		b.OutputTokens += e.OutputTokens
		b.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
		b.CacheReadTokens += e.CacheReadTokens
		b.ExtraTotalTokens += e.ExtraTotalTokens
		b.Cost += e.Cost(mode)
		b.Messages += int64(max(e.Messages, 1))
		if e.Model != "" && !stringSliceContains(b.Models, e.Model) {
			b.Models = append(b.Models, e.Model)
		}
		// Alias the entry's reset pointer instead of copying the time.Time
		// into a local and re-addressing it (which promotes it to the heap).
		// The snapshot is immutable during the call, so the pointer stays
		// valid for the synchronous JSON marshal of the returned blocks.
		if b.UsageLimitReset == nil && e.UsageLimitReset != nil {
			b.UsageLimitReset = e.UsageLimitReset
		}
	}
	b.TotalTokens = b.InputTokens + b.OutputTokens + b.CacheCreationTokens +
		b.CacheReadTokens + b.ExtraTotalTokens

	if b.IsActive {
		if br := blockBurnRate(b, entries); br != nil {
			b.BurnRate = br
			b.Projection = projectBlock(b, br, now)
		}
	}
}

// blockBurnRate mirrors upstream calculate_burn_rate: tokens and cost spread
// over the exact elapsed span from first to last entry. A non-positive span
// yields no rate (upstream returns None), so a burst inside a single instant
// produces neither burn rate nor projection rather than an infinite one.
func blockBurnRate(b *model.Block, entries []*model.Entry) *model.BurnRate {
	if len(entries) == 0 {
		return nil
	}
	minutes := entries[len(entries)-1].Timestamp.Sub(entries[0].Timestamp).Minutes()
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
