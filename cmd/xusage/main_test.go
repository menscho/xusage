package main

import (
	"reflect"
	"testing"
	"time"
)

// TestAdaptivePollerDefaultTiers verifies newAdaptivePoller builds the
// documented backoff ladder: base -> 2.5*base -> 7.5*base -> 15*base.
func TestAdaptivePollerDefaultTiers(t *testing.T) {
	p := newAdaptivePoller(2 * time.Second)
	want := []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}
	if !reflect.DeepEqual(p.steps, want) {
		t.Fatalf("steps = %v, want %v", p.steps, want)
	}
	if p.stabilisation != 3 {
		t.Fatalf("stabilisation = %d, want 3", p.stabilisation)
	}
}

// TestAdaptivePollerStep drives the pure state machine through busy/idle
// sequences and asserts the resulting backoff level. This is the end-to-end
// proof that the stabiliser-backed ladder steps up only after N consecutive
// idle polls and snaps back to base on any activity.
func TestAdaptivePollerStep(t *testing.T) {
	p := newAdaptivePoller(2 * time.Second)
	// 4 tiers, stabilisation 3: it takes 3 idle polls at a tier to step up.
	// Sequence: 9 idle polls should climb 0->1->2->3 then hold at 3.
	idle := func() {
		for i := 0; i < 3; i++ {
			p.step(false)
		}
	}
	p.step(false) // first idle at level 0
	if p.level != 0 || p.idle != 1 {
		t.Fatalf("after 1 idle: level=%d idle=%d, want 0,1", p.level, p.idle)
	}
	if p.level != 0 {
		t.Fatalf("after 1 idle at base: level=%d, want 0", p.level)
	}
	p.step(false)
	if p.level != 0 || p.idle != 2 {
		t.Fatalf("after 2 idle: level=%d idle=%d, want 0,2", p.level, p.idle)
	}
	p.step(false) // third idle -> step to level 1
	if p.level != 1 || p.idle != 0 {
		t.Fatalf("after 3 idle: level=%d idle=%d, want 1,0", p.level, p.idle)
	}

	// Climb to the loosest tier with two more stabilisation cycles.
	idle()
	if p.level != 2 {
		t.Fatalf("after 6 idle: level=%d, want 2", p.level)
	}
	idle()
	if p.level != 3 {
		t.Fatalf("after 9 idle: level=%d, want 3", p.level)
	}

	// Hold at the cap: further idle polls must not exceed the last tier.
	idle()
	if p.level != 3 {
		t.Fatalf("after 12 idle at cap: level=%d, want 3", p.level)
	}

	// A single busy poll snaps all the way back to base.
	p.step(true)
	if p.level != 0 || p.idle != 0 {
		t.Fatalf("after busy from cap: level=%d idle=%d, want 0,0", p.level, p.idle)
	}

	// Mid-climb activity also resets to base, not just from the cap.
	p.step(false)
	p.step(false)
	p.step(false) // level 1
	p.step(false)
	p.step(false)
	p.step(true) // interrupt at level 2 with activity
	if p.level != 0 || p.idle != 0 {
		t.Fatalf("after busy mid-climb: level=%d idle=%d, want 0,0", p.level, p.idle)
	}
}

// TestAdaptivePollerIdleCountReset confirms the idle counter resets on every
// step-up, so intermittent activity keeps the poller at base instead of
// leaking stabilisation credit across cycles.
func TestAdaptivePollerIdleCountReset(t *testing.T) {
	p := newAdaptivePoller(time.Second)
	// Two idle, then activity, then idle again — the count must restart.
	p.step(false)
	p.step(false)
	p.step(true)  // reset
	p.step(false) // first idle of the new cycle
	if p.level != 0 || p.idle != 1 {
		t.Fatalf("after interrupted idle: level=%d idle=%d, want 0,1", p.level, p.idle)
	}
}
