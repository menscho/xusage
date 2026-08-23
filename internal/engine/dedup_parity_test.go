package engine

import (
	"testing"
	"time"

	"xusage/internal/model"
)

// TestUpstreamDedupParity mirrors the upstream adapters/claude/src/lib.rs
// dedup test cases (should_replace_deduped_entry + push_deduped_entry), so the
// Go port cannot drift from the Rust reference:
// https://github.com/ccusage/ccusage/blob/main/rust/adapters/claude/src/lib.rs
//
// Upstream keeps_parent_usage_when_sidechain_replays_message_with_new_request_id:
// a sidechain replay of a known main-chain message ID collapses into the parent
// whatever its request ID, while an unrelated sidechain message survives.
func TestUpstreamDedupParity(t *testing.T) {
	base := time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC)
	mk := func(id, reqID string, sidechain bool, cr, out uint64) model.Entry {
		return model.Entry{
			ID:              id,
			RequestID:       reqID,
			Timestamp:       base,
			Model:           "claude-sonnet-4-20250514",
			OutputTokens:    out,
			CacheReadTokens: cr,
			Messages:        1,
			IsSidechain:     sidechain,
		}
	}

	t.Run("parent survives sidechain replay with new request id", func(t *testing.T) {
		parent := mk("msg-parent", "req-parent", false, 20, 10)
		replay := mk("msg-parent", "req-sidechain-replay", true, 50_000, 10)
		loneSidechain := mk("msg-sidechain-answer", "req-sidechain-answer", true, 700, 30)
		got := dedupe([]model.Entry{parent, replay, loneSidechain})
		if len(got) != 2 {
			t.Fatalf("kept = %d entries, want 2 (upstream expects parent + lone sidechain)", len(got))
		}
		if got[0].RequestID != "req-parent" || got[0].CacheReadTokens != 20 {
			t.Fatalf("canonical parent = req %q cacheRead %d, want req-parent with 20",
				got[0].RequestID, got[0].CacheReadTokens)
		}
	})

	// Upstream refreshes_dedupe_indexes_when_parent_replaces_sidechain_replay:
	// arrival order must not matter — a later main-chain copy retires an
	// earlier sidechain replay and becomes canonical.
	t.Run("replacement updates canonical copy regardless of arrival order", func(t *testing.T) {
		replay := mk("msg-parent", "req-sidechain-replay", true, 50_000, 10)
		parent := mk("msg-parent", "req-parent", false, 20, 10)
		got := dedupe([]model.Entry{replay, parent})
		if len(got) != 1 {
			t.Fatalf("kept = %d entries, want 1", len(got))
		}
		if got[0].IsSidechain || got[0].CacheReadTokens != 20 {
			t.Fatalf("canonical = sidechain=%v cacheRead=%d, want non-sidechain parent with 20",
				got[0].IsSidechain, got[0].CacheReadTokens)
		}
	})
}
