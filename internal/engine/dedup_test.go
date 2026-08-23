package engine

import (
	"testing"
	"time"

	"xusage/internal/model"
)

// dedupEntry builds an entry with the identity fields dedup keys on.
func dedupEntry(id, reqID string, sidechain bool, in, out uint64) model.Entry {
	return model.Entry{
		ID:           id,
		RequestID:    reqID,
		Timestamp:    time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Model:        "m",
		InputTokens:  in,
		OutputTokens: out,
		Messages:     1,
		IsSidechain:  sidechain,
	}
}

func TestDedupSameIDAndRequestIDDuplicate(t *testing.T) {
	tests := []struct {
		name  string
		first model.Entry
		dup   model.Entry
		want  int // kept entries after dedup
	}{
		{
			name:  "identical ID+requestID collapses to one entry",
			first: dedupEntry("msg_1", "req_1", false, 10, 5),
			dup:   dedupEntry("msg_1", "req_1", false, 10, 5),
			want:  1,
		},
		{
			name:  "same ID different requestID is NOT a duplicate",
			first: dedupEntry("msg_1", "req_1", false, 10, 5),
			dup:   dedupEntry("msg_1", "req_2", false, 10, 5),
			want:  2,
		},
		{
			name:  "different ID same requestID is NOT a duplicate",
			first: dedupEntry("msg_1", "req_1", false, 10, 5),
			dup:   dedupEntry("msg_2", "req_1", false, 10, 5),
			want:  2,
		},
		{
			name:  "same ID empty requestIDs collide",
			first: dedupEntry("msg_1", "", false, 10, 5),
			dup:   dedupEntry("msg_1", "", false, 7, 7),
			want:  1,
		},
		{
			name:  "empty vs non-empty requestID do not collide",
			first: dedupEntry("msg_1", "", false, 10, 5),
			dup:   dedupEntry("msg_1", "req_9", false, 10, 5),
			want:  2,
		},
		{
			name:  "duplicate across providers does not collapse",
			first: dedupEntry("msg_1", "req_1", false, 10, 5),
			dup: func() model.Entry {
				e := dedupEntry("msg_1", "req_1", false, 10, 5)
				e.Provider = "other"
				return e
			}(),
			want: 2,
		},
		{
			name:  "empty message IDs never dedup",
			first: dedupEntry("", "req_1", false, 10, 5),
			dup:   dedupEntry("", "req_1", false, 99, 99),
			want:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupe([]model.Entry{tt.first, tt.dup})
			if len(got) != tt.want {
				t.Fatalf("kept = %d entries, want %d", len(got), tt.want)
			}
		})
	}
}

func TestDedupPreferenceRules(t *testing.T) {
	fast := dedupEntry("m", "r", false, 100, 0)
	fast.SpeedFast = true
	tests := []struct {
		name     string
		a, b     model.Entry // same provider+ID+requestID
		wantTok  uint64      // expected canonical copy's token count
		wantFast bool
	}{
		{
			name:    "higher total tokens wins",
			a:       dedupEntry("m", "r", false, 100, 0),
			b:       dedupEntry("m", "r", false, 250, 0),
			wantTok: 250,
		},
		{
			name:     "fast speed wins token tie",
			a:        fast,
			b:        dedupEntry("m", "r", false, 100, 0),
			wantTok:  100,
			wantFast: true,
		},
		{
			name:    "equal copies keep incumbent",
			a:       dedupEntry("m", "r", false, 50, 50),
			b:       dedupEntry("m", "r", false, 50, 50),
			wantTok: 100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupe([]model.Entry{tt.a, tt.b})
			if len(got) != 1 {
				t.Fatalf("kept = %d entries, want 1", len(got))
			}
			if got[0].TotalTokens() != tt.wantTok || got[0].SpeedFast != tt.wantFast {
				t.Fatalf("canonical tokens=%d fast=%v, want tokens=%d fast=%v",
					got[0].TotalTokens(), got[0].SpeedFast, tt.wantTok, tt.wantFast)
			}
		})
	}
}

func TestDedupSidechainReplayRules(t *testing.T) {
	t.Run("sidechain replay of known non-sidechain ID collapses regardless of request ID", func(t *testing.T) {
		parent := dedupEntry("msg_1", "req_A", false, 100, 10)
		replay := dedupEntry("msg_1", "req_B", true, 5, 5)
		got := dedupe([]model.Entry{parent, replay})
		if len(got) != 1 {
			t.Fatalf("kept = %d entries, want 1", len(got))
		}
		if got[0].IsSidechain || got[0].TotalTokens() != 110 {
			t.Fatalf("non-sidechain parent should stay canonical, got sidechain=%v tokens=%d",
				got[0].IsSidechain, got[0].TotalTokens())
		}
	})

	t.Run("bigger sidechain still loses to smaller non-sidechain parent", func(t *testing.T) {
		parent := dedupEntry("msg_1", "req_A", false, 10, 0)
		bigReplay := dedupEntry("msg_1", "req_B", true, 500, 500)
		got := dedupe([]model.Entry{parent, bigReplay})
		if len(got) != 1 {
			t.Fatalf("kept = %d entries, want 1", len(got))
		}
		if got[0].IsSidechain {
			t.Fatal("preference order violated: non-sidechain must beat sidechain even with fewer tokens")
		}
	})

	t.Run("second sidechain replay of the same ID collapses into the first", func(t *testing.T) {
		// Upstream /btw replays re-log the parent message under fresh request
		// IDs; among sidechain copies of one message ID only the canonical
		// (preferred) copy survives.
		s1 := dedupEntry("msg_1", "req_A", true, 10, 0)
		s2 := dedupEntry("msg_1", "req_B", true, 20, 0)
		got := dedupe([]model.Entry{s1, s2})
		if len(got) != 1 {
			t.Fatalf("kept = %d entries, want 1", len(got))
		}
		if !got[0].IsSidechain || got[0].TotalTokens() != 20 {
			t.Fatalf("canonical sidechain should be the preferred copy, got tokens=%d",
				got[0].TotalTokens())
		}
	})

	t.Run("sidechain before non-sidechain still yields parent canonical", func(t *testing.T) {
		replay := dedupEntry("msg_1", "req_B", true, 5, 5)
		parent := dedupEntry("msg_1", "req_C", false, 100, 10)
		got := dedupe([]model.Entry{replay, parent}) // reversed arrival order
		if len(got) != 1 {
			t.Fatalf("kept = %d entries, want 1", len(got))
		}
		if got[0].IsSidechain || got[0].TotalTokens() != 110 {
			t.Fatalf("parent should replace sidechain as canonical, got sidechain=%v tokens=%d",
				got[0].IsSidechain, got[0].TotalTokens())
		}
	})

	t.Run("replay collapses even when only the sidechain is seen first and no request IDs match later", func(t *testing.T) {
		replay := dedupEntry("msg_1", "req_B", true, 3, 3)
		a := dedupEntry("msg_1", "req_X", false, 30, 0)
		b := dedupEntry("msg_1", "req_Y", false, 20, 0) // second distinct non-sidechain request
		got := dedupe([]model.Entry{replay, a, b})
		if len(got) != 2 {
			t.Fatalf("kept = %d entries, want 2 (replay + one non-sidechain per request)", len(got))
		}
		for _, e := range got {
			if e.IsSidechain {
				t.Fatal("sidechain replay should have been folded into the first non-sidechain copy")
			}
		}
	})
}

func TestPreferTiebreakers(t *testing.T) {
	base := dedupEntry("m", "r", false, 10, 0)
	sideBase := base
	sideBase.IsSidechain = true
	fast := base
	fast.SpeedFast = true
	tests := []struct {
		name string
		c, x model.Entry
		want bool
	}{
		{"candidate non-sidechain beats sidechain", base, sideBase, true},
		{"candidate sidechain loses to non-sidechain", sideBase, base, false},
		{"more tokens wins", dedupEntry("m", "r", false, 20, 0), base, true},
		{"fewer tokens loses", base, dedupEntry("m", "r", false, 20, 0), false},
		{"fast beats standard on tie", fast, base, true},
		{"standard loses to fast on tie", base, fast, false},
		{"fully equal keeps incumbent", base, base, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, x := tt.c, tt.x
			if got := prefer(&c, &x); got != tt.want {
				t.Fatalf("prefer() = %v, want %v", got, tt.want)
			}
		})
	}
}
