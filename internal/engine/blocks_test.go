package engine

import (
	"testing"
	"time"

	"xusage/internal/model"
)

// blockTestBase keeps block math independent of the wall clock.
var blockTestBase = time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)

func at(hour, min int) time.Time {
	return blockTestBase.Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute)
}

func mkEntry(ts time.Time, input, output uint64) model.Entry {
	return model.Entry{
		Timestamp:    ts,
		Model:        "claude-sonnet-4-20250514",
		InputTokens:  input,
		OutputTokens: output,
		Messages:     1,
		CostAuto:     3.0,
		CostCalc:     3.0,
	}
}

func mkSnapshot(entries ...model.Entry) *Snapshot {
	return &Snapshot{Entries: entries, BuiltAt: blockTestBase}
}

// farFuture makes every historical block inactive.
var farFuture = blockTestBase.Add(48 * time.Hour)

func TestBlocksSingleBlock(t *testing.T) {
	snap := mkSnapshot(
		mkEntry(at(10, 10), 100, 50),
		mkEntry(at(10, 40), 200, 100),
		mkEntry(at(12, 0), 300, 150), // within 5h of start and of previous entry
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, farFuture)

	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	b := blocks[0]
	if want := at(10, 0); !b.StartTime.Equal(want) {
		t.Errorf("start = %v, want floor-to-hour %v", b.StartTime, want)
	}
	if want := at(15, 0); !b.EndTime.Equal(want) {
		t.Errorf("end = %v, want %v", b.EndTime, want)
	}
	if b.ActualEndTime == nil || !b.ActualEndTime.Equal(at(12, 0)) {
		t.Errorf("actualEnd = %v, want %v", b.ActualEndTime, at(12, 0))
	}
	if b.IsGap || b.IsActive {
		t.Errorf("block misflagged: gap=%v active=%v", b.IsGap, b.IsActive)
	}
	if b.ID != "2025-06-10T10:00:00.000Z" {
		t.Errorf("id = %q", b.ID)
	}
	if b.InputTokens != 600 || b.OutputTokens != 300 {
		t.Errorf("tokens = %d/%d, want 600/300", b.InputTokens, b.OutputTokens)
	}
	if b.Cost != 9.0 {
		t.Errorf("cost = %v, want 9.0", b.Cost)
	}
	if b.Messages != 3 {
		t.Errorf("messages = %d, want 3", b.Messages)
	}
	if len(b.Models) != 1 || b.Models[0] != "claude-sonnet-4-20250514" {
		t.Errorf("models = %v", b.Models)
	}
	if len(b.Entries) != 3 {
		t.Errorf("entries = %d, want 3", len(b.Entries))
	}
}

func TestBlocksModelsKeepFirstSeenOrder(t *testing.T) {
	// Upstream pushes models in encounter order (dedup by set); it never
	// sorts them alphabetically.
	m := func(ts time.Time, name string) model.Entry {
		e := mkEntry(ts, 100, 0)
		e.Model = name
		return e
	}
	snap := mkSnapshot(
		m(at(10, 0), "zeta-model"),
		m(at(10, 5), "alpha-model"),
		m(at(10, 10), "zeta-model"), // repeat must not re-enter the list
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, farFuture)
	want := []string{"zeta-model", "alpha-model"}
	if len(blocks[0].Models) != len(want) {
		t.Fatalf("models = %v, want %v", blocks[0].Models, want)
	}
	for i, w := range want {
		if blocks[0].Models[i] != w {
			t.Errorf("models[%d] = %q, want %q (first-seen order)", i, blocks[0].Models[i], w)
		}
	}
}

func TestBlocksMultiBlockNoGap(t *testing.T) {
	// 15:01 is >5h after the block START but only ~1h after the previous
	// entry: upstream closes the block WITHOUT inserting a gap.
	snap := mkSnapshot(
		mkEntry(at(10, 0), 100, 0),
		mkEntry(at(14, 0), 100, 0),
		mkEntry(at(15, 1), 100, 0),
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, farFuture)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (no gap)", len(blocks))
	}
	for _, b := range blocks {
		if b.IsGap {
			t.Errorf("unexpected gap block %s", b.ID)
		}
	}
	if want := at(15, 0); !blocks[1].StartTime.Equal(want) {
		t.Errorf("second block start = %v, want %v", blocks[1].StartTime, want)
	}
	if !blocks[0].ActualEndTime.Equal(at(14, 0)) {
		t.Errorf("first block actualEnd = %v, want 14:00", blocks[0].ActualEndTime)
	}
}

func TestBlocksBoundaryExactlyFiveHoursStays(t *testing.T) {
	// An entry exactly 5h after the block start does not open a new block
	// (upstream splits only on strictly-greater gaps).
	snap := mkSnapshot(
		mkEntry(at(10, 0), 100, 0),
		mkEntry(at(15, 0), 100, 0),
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, farFuture)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (boundary is inclusive)", len(blocks))
	}
	if len(blocks[0].Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(blocks[0].Entries))
	}
}

func TestBlocksNewBlockAfterFiveHoursSinceStart(t *testing.T) {
	// 5h past both the block START and the previous entry opens a new block
	// AND inserts an explicit gap covering [last+5h, next entry).
	snap := mkSnapshot(
		mkEntry(at(10, 0), 100, 0),
		mkEntry(at(15, 1), 100, 0), // 5h1m after start, 5h1m after previous
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, farFuture)
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (block, gap, block)", len(blocks))
	}
	if !blocks[1].IsGap {
		t.Errorf("middle block should be a gap")
	}
	if want := at(15, 0); !blocks[2].StartTime.Equal(want) {
		t.Errorf("second block start = %v, want %v", blocks[2].StartTime, want)
	}
}

func TestBlocksGapInsertion(t *testing.T) {
	snap := mkSnapshot(
		mkEntry(at(10, 10), 100, 0),
		mkEntry(at(10, 20), 100, 0),
		mkEntry(at(16, 30), 100, 0), // >5h after previous entry
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, farFuture)

	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (block, gap, block)", len(blocks))
	}
	gap := blocks[1]
	if !gap.IsGap {
		t.Fatalf("middle block is not a gap: %+v", gap)
	}
	if want := "gap-2025-06-10T15:20:00.000Z"; gap.ID != want {
		t.Errorf("gap id = %q, want %q", gap.ID, want)
	}
	// Upstream create_gap_block: start = last entry + duration, end = next entry.
	if want := at(15, 20); !gap.StartTime.Equal(want) {
		t.Errorf("gap start = %v, want last+5h %v", gap.StartTime, want)
	}
	if want := at(16, 30); !gap.EndTime.Equal(want) {
		t.Errorf("gap end = %v, want next entry %v (not floored)", gap.EndTime, want)
	}
	if gap.ActualEndTime != nil || gap.IsActive {
		t.Errorf("gap must be inactive with no actual end")
	}
	if gap.TotalTokens != 0 || len(gap.Entries) != 0 || gap.Cost != 0 {
		t.Errorf("gap must stay empty, got tokens=%d cost=%v", gap.TotalTokens, gap.Cost)
	}
	if want := at(16, 0); !blocks[2].StartTime.Equal(want) {
		t.Errorf("post-gap block start = %v, want %v", blocks[2].StartTime, want)
	}
	// Entries must stay with their own blocks (no slice aliasing).
	if len(blocks[0].Entries) != 2 {
		t.Errorf("first block entries = %d, want 2 (aliasing bug)", len(blocks[0].Entries))
	}
	if len(blocks[2].Entries) != 1 {
		t.Errorf("last block entries = %d, want 1", len(blocks[2].Entries))
	}
	if blocks[0].InputTokens != 200 {
		t.Errorf("first block input = %d, want 200 (clobbered by aliasing?)", blocks[0].InputTokens)
	}
}

func TestBlocksEmpty(t *testing.T) {
	snap := mkSnapshot()
	if blocks := snap.Blocks(&Opts{}, DefaultBlockHours, farFuture); blocks != nil {
		t.Errorf("empty snapshot should yield nil blocks, got %v", blocks)
	}
	if b := snap.ActiveBlock(&Opts{}, time.Now()); b != nil {
		t.Errorf("empty snapshot should have no active block, got %+v", b)
	}
}

func TestBlocksActiveBurnAndProjection(t *testing.T) {
	// Block 10:00-15:00, entries at 10:00 and 10:30, now = 11:00.
	// Elapsed span (first->last) = 30min; 1500 tokens -> 50 tokens/min;
	// cost 6.0 -> 12/h; remaining = 240min.
	now := at(11, 0)
	snap := mkSnapshot(
		mkEntry(at(10, 0), 1000, 500),
		mkEntry(at(10, 30), 0, 0),
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, now)
	if len(blocks) != 1 || !blocks[0].IsActive {
		t.Fatalf("want one active block, got %+v", blocks)
	}
	b := blocks[0]
	if b.BurnRate == nil {
		t.Fatal("active block missing burn rate")
	}
	if got := b.BurnRate.TokensPerMinute; got != 50.0 {
		t.Errorf("tokens/min = %v, want 50", got)
	}
	if got := b.BurnRate.CostPerHour; got != 12.0 {
		t.Errorf("cost/hour = %v, want 12 (6.0 over a 30-minute span)", got)
	}
	if b.Projection == nil {
		t.Fatal("active block missing projection")
	}
	if got := b.Projection.TotalTokens; got != 1500+50*240 {
		t.Errorf("projected tokens = %d, want %d", got, 1500+50*240)
	}
	if got := b.Projection.TotalCost; got != 54.0 {
		t.Errorf("projected cost = %v, want 54 (6 + 12/60*240)", got)
	}
	if got := b.Projection.RemainingMinutes; got != 240 {
		t.Errorf("remaining minutes = %d, want 240", got)
	}
}

func TestBlocksBurnRateSpreadsOverExactSpan(t *testing.T) {
	// Upstream divides by the exact first->last span (30s here): 1500 tokens
	// over 0.5 minutes -> 3000 tokens/min; cost 2×3.0=6.0 -> 720/h. There is
	// no 1-minute clamp.
	now := blockTestBase.Add(time.Minute)
	snap := mkSnapshot(
		withTS(mkEntry(at(10, 0), 900, 600), blockTestBase),
		withTS(mkEntry(at(10, 0), 0, 0), blockTestBase.Add(30*time.Second)),
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, now)
	if len(blocks) != 1 || blocks[0].BurnRate == nil {
		t.Fatalf("want one block with burn rate, got %+v", blocks)
	}
	if got := blocks[0].BurnRate.TokensPerMinute; got != 3000 {
		t.Errorf("tokens/min = %v, want 3000 (exact 0.5-minute span)", got)
	}
	if got := blocks[0].BurnRate.CostPerHour; got != 720.0 {
		t.Errorf("cost/hour = %v, want 720", got)
	}
}

func TestBlocksZeroSpanNoBurnRateOrProjection(t *testing.T) {
	// All entries share one instant: upstream calculate_burn_rate returns
	// None, so the active block carries neither burnRate nor projection
	// instead of an infinite rate.
	now := blockTestBase.Add(10 * time.Minute)
	snap := mkSnapshot(
		withTS(mkEntry(at(10, 0), 900, 600), blockTestBase),
		withTS(mkEntry(at(10, 0), 100, 0), blockTestBase),
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, now)
	if len(blocks) != 1 || !blocks[0].IsActive {
		t.Fatalf("want one active block, got %+v", blocks)
	}
	if blocks[0].BurnRate != nil || blocks[0].Projection != nil {
		t.Errorf("zero-span block must omit burnRate/projection, got %+v", blocks[0])
	}
}

func TestBlocksInactiveWhenNowPastEnd(t *testing.T) {
	// The last block must not stay active once now passes start+5h even
	// though it is still the most recent block.
	now := at(16, 1)
	snap := mkSnapshot(mkEntry(at(10, 30), 100, 0)) // block 10:00-15:00
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, now)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0].IsActive {
		t.Error("block is active although now > end")
	}
	if blocks[0].BurnRate != nil || blocks[0].Projection != nil {
		t.Error("inactive block must not carry burn rate or projection")
	}
	if b := snap.ActiveBlock(&Opts{}, now); b != nil {
		t.Errorf("ActiveBlock = %+v, want nil", b)
	}
}

func TestBlocksActiveIsLastBlockOnly(t *testing.T) {
	// Two historical blocks plus a live one; only the newest is active.
	now := at(22, 0)
	snap := mkSnapshot(
		mkEntry(at(9, 0), 100, 0),   // block 09:00-14:00
		mkEntry(at(15, 0), 100, 0),  // gap [14:00,15:00) + block 15:00-20:00
		mkEntry(at(21, 30), 100, 0), // gap [20:00,21:30) + block 21:00-02:00
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, now)
	if len(blocks) != 5 {
		t.Fatalf("got %d blocks, want 5 (2 gaps)", len(blocks))
	}
	active := 0
	for i, b := range blocks {
		if b.IsActive {
			active++
			if i != 4 {
				t.Errorf("block %d (start %v) is active; only the last may be", i, b.StartTime)
			}
		}
	}
	if active != 1 {
		t.Errorf("active blocks = %d, want 1", active)
	}
	got := snap.ActiveBlock(&Opts{}, now)
	if got == nil || !got.StartTime.Equal(at(21, 0)) {
		t.Errorf("ActiveBlock start = %v, want 21:00", got)
	}
}

func TestBlocksUsageLimitResetFirstWins(t *testing.T) {
	// Upstream folds usage_limit_reset_time with .or() — the FIRST entry
	// carrying a reset wins, later ones never overwrite it.
	r1 := at(11, 0)
	r2 := at(12, 30)
	now := at(10, 30)
	snap := mkSnapshot(
		mkEntry(at(10, 0), 10, 0),
		withReset(mkEntry(at(10, 5), 10, 0), &r2),
		withReset(mkEntry(at(10, 10), 10, 0), &r1),
	)
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, now)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	b := blocks[0]
	if b.UsageLimitReset == nil || !b.UsageLimitReset.Equal(r2) {
		t.Errorf("usageLimitReset = %v, want first-seen %v", b.UsageLimitReset, r2)
	}
}

func TestBlocksUsageLimitResetAbsent(t *testing.T) {
	snap := mkSnapshot(mkEntry(at(10, 0), 10, 0))
	blocks := snap.Blocks(&Opts{}, DefaultBlockHours, at(10, 30))
	if len(blocks) != 1 || blocks[0].UsageLimitReset != nil {
		t.Errorf("want nil usageLimitReset, got %v", blocks[0].UsageLimitReset)
	}
}

func TestBlocksHonorsOptsSessionAndTZFilters(t *testing.T) {
	// Blocks runs over the filtered view: session narrows entries, and
	// since/until are interpreted in the option timezone.
	e1 := mkEntry(at(10, 0), 100, 0)
	e1.SessionID = "s1"
	e2 := mkEntry(at(11, 0), 100, 0)
	e2.SessionID = "s2"
	snap := mkSnapshot(e1, e2)

	blocks := snap.Blocks(&Opts{Session: "s2"}, DefaultBlockHours, farFuture)
	if len(blocks) != 1 || len(blocks[0].Entries) != 1 || blocks[0].InputTokens != 100 {
		t.Fatalf("session filter: want 1 block holding only s2, got %+v", blocks)
	}

	// In a +04 zone, since=June 10 starts at 20:00Z June 9: the 19:00Z
	// entry (Jun 9 23:00 local) is excluded, the 21:00Z one (Jun 10 01:00
	// local) kept.
	loc := time.FixedZone("test", 4*3600)
	snapTZ := mkSnapshot(
		mkEntry(time.Date(2025, 6, 9, 19, 0, 0, 0, time.UTC), 100, 0),
		mkEntry(time.Date(2025, 6, 9, 21, 0, 0, 0, time.UTC), 100, 0),
	)
	blocks = snapTZ.Blocks(&Opts{TZ: loc, Since: "2025-06-10"}, DefaultBlockHours, farFuture)
	if len(blocks) != 1 || blocks[0].InputTokens != 100 {
		t.Errorf("since=2025-06-10 in +04 must keep only the 21:00Z entry, got %+v", blocks)
	}
	blocks = snapTZ.Blocks(&Opts{TZ: loc, Since: "2025-06-09"}, DefaultBlockHours, farFuture)
	if len(blocks) != 1 || blocks[0].InputTokens != 200 {
		t.Errorf("since=2025-06-09 in +04 must keep both entries, got %+v", blocks)
	}
}

// --- small helpers -----------------------------------------------------

func withTS(e model.Entry, ts time.Time) model.Entry {
	e.Timestamp = ts
	return e
}

func withReset(e model.Entry, ts *time.Time) model.Entry {
	e.UsageLimitReset = ts
	return e
}
