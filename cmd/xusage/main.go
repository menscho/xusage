// Command xusage serves a local dashboard over coding-agent usage logs.
//
// It discovers every registered source's data directory, parses and prices
// usage entries into an in-memory snapshot, and exposes the analytics API plus
// the embedded web UI on 127.0.0.1 only.
package main

import (
	"log"
	"time"

	"xusage/internal/engine"
	_ "xusage/internal/sources/all" // link every source adapter into the binary
)

func main() {
	osExit(run())
}

// adaptivePoller drives engine.Refresh() on a cadence that relaxes while the
// data is idle and snaps back the instant anything changes.
//
// The refresh ticker lives in the engine/server and is fixed-interval by design
// (server.Run -> engine.StartPolling), so it cannot be adapted from this
// package without touching those files. This loop is therefore handed the
// authoritative cadence: it calls Refresh() directly and steps through
// base -> 2.5*base -> 7.5*base -> 15*base (2s -> 5s -> 15s -> 30s for the
// default) after a run of idle polls, dropping straight back to base on any
// change. The server's own ticker is reduced to a slow backstop (see
// adaptiveBackstop in flags.go).
//
// Change detection reuses the store's own per-file-cache short-circuit signal:
// the engine increments ParseCalls only when a file's cache is missed (its
// mtime/size changed), so it reliably flags modified and added files; entry
// length changes on addition/removal. Together they give a complete, zero-extra
// I/O "did anything change" test that stays consistent with how the store
// itself decides a poll did work.
type adaptivePoller struct {
	base          time.Duration
	steps         []time.Duration // ascending backoff tiers
	stabilisation int             // consecutive idle polls before stepping up

	primed    bool
	busy      bool // previous poll's busy state; transitions are logged
	level     int
	idle      int
	lastParse int64
	lastLen   int
}

func newAdaptivePoller(base time.Duration) *adaptivePoller {
	return &adaptivePoller{
		base:          base,
		steps:         []time.Duration{base, base * 5 / 2, base * 15 / 2, base * 15},
		stabilisation: 3,
	}
}

func (p *adaptivePoller) run(stop <-chan struct{}) {
	// Reuse one timer across ticks instead of allocating a fresh
	// runtimeTimer per poll via time.After. The channel is drained by the
	// select below before every Reset, so Reset is always safe without a
	// Stop/drain dance. defer stops it on the shutdown path.
	timer := time.NewTimer(p.steps[p.level])
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}

		// Refresh returns the snapshot it just stored; reading its entry
		// length here avoids a second atomic load through engine.Get().
		snap := engine.Refresh()

		parse := engine.ParseCalls()
		n := len(snap.Entries)
		// snap is a *Snapshot pointing at the engine's just-stored current
		// snapshot; the engine keeps its own reference, so our local pin is only
		// needed for that len() read. Drop it now: if the server's backstop ticker
		// fires a refresh while we sleep (up to 30s at the loosest tier), the old
		// snapshot otherwise stays pinned in heap until our next poll overwrites
		// this local — needlessly doubling live entries for the idle window.
		snap = nil
		if !p.primed {
			// First tick just records the post-start baseline; the store has
			// now done its initial load, so this is the reference state.
			p.primed = true
		} else {
			nowBusy := parse != p.lastParse || n != p.lastLen
			if nowBusy != p.busy {
				// Log only on a busy/idle transition so a long-running idle
				// stretch (or a busy one) does not spam the log every poll.
				p.busy = nowBusy
				if nowBusy {
					log.Printf("[xusage] data changed — polling every %s", p.steps[0])
				} else {
					log.Printf("[xusage] idle — polling every %s", p.steps[p.level])
				}
			}
			p.step(nowBusy)
		}
		p.lastParse = parse
		p.lastLen = n

		// step() may have advanced the level; arm the reused timer for the
		// (possibly loosened) next tier.
		timer.Reset(p.steps[p.level])
	}
}

// step updates the backoff level and idle counter from the latest poll's busy
// state, pure of any timer so it can be unit-tested without a real clock.
//
// Activity snaps straight back to the tightest cadence (level 0). Each
// consecutive idle poll increments the stabiliser; once it reaches the
// stabilisation count the level steps up one tier and the counter resets,
// capping at the loosest tier. Keeping this state machine out of run() lets
// tests drive it deterministically while run() owns only the wake timing.
func (p *adaptivePoller) step(busy bool) {
	if busy {
		// Activity: drop straight back to the tightest cadence.
		p.level = 0
		p.idle = 0
		return
	}
	if p.level < len(p.steps)-1 {
		// Idle another tick at this tier; step up once it stabilises.
		p.idle++
		if p.idle >= p.stabilisation {
			p.level++
			p.idle = 0
		}
	}
}
