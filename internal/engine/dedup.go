// Package engine — deduplication of loaded entries.
//
// Port of ccusage's Claude dedup rules: an entry duplicates another when it
// carries the same message ID AND the same request ID within a provider (the
// same logical request seen multiple times — replays, streamed snapshots).
// A sidechain entry whose message ID matches ANY non-sidechain entry is also
// a duplicate replay of that parent request, whatever its request ID.
//
// The canonical copy prefers non-sidechain over sidechain, then more total
// tokens, then fast-speed presence. Entries without a message ID never dedup.
package engine

import (
	"sort"

	"xusage/internal/model"
)

// dedupe removes duplicate entries, keeping one canonical copy per logical
// request. Entries without a message ID are kept as-is.
func dedupe(entries []model.Entry) []model.Entry {
	// Entries sharing a provider+message ID form one group: main-chain copies
	// collapse by request ID; sidechain copies collapse to a single canonical
	// copy per message ID (upstream /btw replays carry new request IDs) until
	// a main-chain copy shows up and retires them as replays.
	type group struct {
		main map[string]int // request ID -> position in kept
		side int            // canonical sidechain copy position, -1 = none
	}
	groups := map[string]*group{}
	dropped := map[int]bool{}
	var kept []model.Entry

	keep := func(e model.Entry, seen map[string]int) {
		pos, ok := seen[e.RequestID]
		if !ok {
			seen[e.RequestID] = len(kept)
			kept = append(kept, e)
			return
		}
		if prefer(&e, &kept[pos]) {
			kept[pos] = e
		}
	}

	for _, e := range entries {
		if e.ID == "" {
			kept = append(kept, e) // unidentifiable entries never dedup
			continue
		}
		idKey := e.Provider + "\x00" + e.ID
		g := groups[idKey]
		if g == nil {
			g = &group{main: map[string]int{}, side: -1}
			groups[idKey] = g
		}
		if e.IsSidechain {
			if len(g.main) > 0 {
				continue // replay of an already-known main-chain request
			}
			if g.side >= 0 {
				if prefer(&e, &kept[g.side]) {
					kept[g.side] = e // better sidechain copy replaces in place
				}
				continue
			}
			g.side = len(kept)
			kept = append(kept, e)
			continue
		}
		if g.side >= 0 {
			dropped[g.side] = true // main-chain copy supersedes sidechain replays
			g.side = -1
		}
		keep(e, g.main)
	}

	if len(dropped) == 0 {
		return kept
	}
	out := make([]model.Entry, 0, len(kept)-len(dropped))
	for i, e := range kept {
		if !dropped[i] {
			out = append(out, e)
		}
	}
	return out
}

// prefer reports whether candidate c should replace incumbent x as canonical:
// non-sidechain first, then more total tokens, then fast-speed presence.
func prefer(c, x *model.Entry) bool {
	if c.IsSidechain != x.IsSidechain {
		return x.IsSidechain // non-sidechain wins
	}
	tc, tx := c.TotalTokens(), x.TotalTokens()
	if tc != tx {
		return tc > tx
	}
	return c.SpeedFast && !x.SpeedFast
}

// sortEntries orders by timestamp ascending (stable for equal timestamps).
func sortEntries(entries []model.Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
}
