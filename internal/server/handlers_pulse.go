// Package server — /api/pulse and extended /api/live payloads powering the
// redesigned sidebar badges and the live pulse banner.
package server

import (
	"net/http"
	"time"

	"xusage/internal/engine"
)

// pulsePayload is the single compact poll the sidebar and pulse banner use:
// all-time totals, active-run state, detected CLIs, a 7-day token series,
// and the most recent entry's attribution.
type pulsePayload struct {
	GeneratedAt string                  `json:"generatedAt"`
	Tokens      uint64                  `json:"tokens"`
	Cost        float64                 `json:"cost"`
	Messages    int64                   `json:"messages"`
	Sessions    int64                   `json:"sessions"`
	Days        int64                   `json:"days"`
	ActiveRun   bool                    `json:"activeRun"`
	Clis        []engine.ProviderStatus `json:"clis"`
	Last7       []engine.TrendPoint     `json:"last7"`
	Latest      *latestEntry            `json:"latest"`
}

type latestEntry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	At       string `json:"at"`
}

func latestOf(snap *engine.Snapshot) *latestEntry {
	if len(snap.Entries) == 0 {
		return nil
	}
	e := snap.Entries[len(snap.Entries)-1]
	return &latestEntry{
		Provider: e.Provider,
		Model:    e.Model,
		At:       e.Timestamp.UTC().Format(time.RFC3339Nano),
	}
}

// dateKey is a location-independent civil date. It lets last7Series bucket
// entries by their located civil day without formatting a string (and without
// a map) per entry — a poll now allocates nothing per entry regardless of
// corpus size. time.Month compares by its underlying int.
type dateKey struct {
	y int
	m time.Month
	d int
}

func last7Series(snap *engine.Snapshot) []engine.TrendPoint {
	loc := time.Local
	today := time.Now().In(loc)
	days := make([]engine.TrendPoint, 7)
	// 7 civil-day buckets ending today; precompute each bucket's civil date so
	// entries match by equality on (y, m, d) instead of a formatted string.
	var bounds [7]dateKey
	for i := 0; i < 7; i++ {
		d := today.AddDate(0, 0, i-6)
		bounds[i].y, bounds[i].m, bounds[i].d = d.Date()
		days[i].Date = d.Format("2006-01-02")
	}
	// Entries are timestamp-sorted ascending (store invariant, asserted in
	// store_test.go). The 7-day window starts at the civil day "today-6", so
	// every earlier entry falls outside all buckets — skip it with one cheap
	// Before() check instead of a .In().Date() zone lookup. On a large corpus
	// the historical majority is bypassed, leaving the zone conversion only
	// for the few thousand entries actually inside the window.
	windowStart := time.Date(bounds[0].y, bounds[0].m, bounds[0].d, 0, 0, 0, 0, loc)
	for i := range snap.Entries {
		e := &snap.Entries[i]
		if e.Timestamp.Before(windowStart) {
			continue
		}
		ey, em, ed := e.Timestamp.In(loc).Date()
		for j := 0; j < 7; j++ {
			if ey == bounds[j].y && em == bounds[j].m && ed == bounds[j].d {
				days[j].Tokens += e.TotalTokens()
				days[j].Cost += e.CostAuto
				break
			}
		}
	}
	return days
}

func buildPulse(snap *engine.Snapshot) pulsePayload {
	// One Opts reused for both aggregations: Summary and ActiveBlock are
	// called sequentially and neither retains the pointer, so a single
	// allocation covers both (each previously allocated its own).
	o := &engine.Opts{}
	now := time.Now()
	sum := snap.Summary(o)
	active := snap.ActiveBlock(o, now) != nil
	// Clis aliases the snapshot's own Providers slice: the two structs are
	// field- and tag-identical, so the JSON output is unchanged while the
	// per-poll copy (one allocation + a loop) disappears. Safe because a
	// Snapshot is immutable after construction and writeJSON only reads it.
	return pulsePayload{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Tokens:      sum.TotalTokens,
		Cost:        sum.Cost,
		Messages:    sum.Messages,
		Sessions:    sum.Sessions,
		Days:        sum.Days,
		ActiveRun:   active,
		Clis:        snap.Providers,
		Last7:       last7Series(snap),
		Latest:      latestOf(snap),
	}
}

// pulseQCache caches the serialized /api/pulse body per snapshot. The pulse
// payload is a pure function of the snapshot — handlePulse ignores the
// request — so a single key per snapshot suffices and a new snapshot (after a
// refresh) orphans the previous entry at once. This mirrors vendorsQCache for
// the vendor/model endpoints: on a hit it skips both the Summary+Blocks
// aggregation and the JSON encoding. Without it, every /api/pulse call —
// including repeated rescans against an unchanged snapshot (Refresh returns
// the same pointer on the idle short-circuit) — recomputes and re-encodes
// identical bytes.
var pulseQCache = &qCache{body: map[string][]byte{}}

func handlePulse(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	if body, ok := pulseQCache.get(snap, ""); ok {
		writeCachedJSON(w, body)
		return
	}
	body := encodeJSON(buildPulse(snap))
	pulseQCache.put(snap, "", body)
	writeCachedJSON(w, body)
}
