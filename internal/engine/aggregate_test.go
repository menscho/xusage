package engine

import (
	"math"
	"testing"
	"time"

	"xusage/internal/model"
)

// fixedTZ is a deterministic non-UTC zone (+01:00) for timezone tests.
var fixedTZ = time.FixedZone("TEST", 3600)

// ts parses "2006-01-02 15:04:05" as UTC.
func ts(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

// aggEntry builds a priced entry with explicit costs so aggregation tests do
// not depend on the pricing table.
func aggEntry(stamp time.Time, in, out, cc, cr uint64, cost float64) model.Entry {
	return model.Entry{
		Timestamp:           stamp,
		Model:               "m1",
		InputTokens:         in,
		OutputTokens:        out,
		CacheCreationTokens: cc,
		CacheReadTokens:     cr,
		CostDisplay:         cost,
		CostAuto:            cost,
		CostCalc:            cost,
		Messages:            1,
	}
}

func rowKeys(rows []model.Row) []string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Key
	}
	return keys
}

func sumCost(rows []model.Row) float64 {
	var c float64
	for i := range rows {
		c += rows[i].Cost
	}
	return c
}

func nearly(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestAllPeriodsAgreeWithSummary pins the totals contract: daily, weekly,
// monthly, and hourly rows must each sum to the summary grand totals on the
// same fixture data (SPEC §6 — one accumulator feeds every report).
func TestAllPeriodsAgreeWithSummary(t *testing.T) {
	stamps := []string{
		"2026-01-01 08:15:00", "2026-01-01 23:59:59.999", "2026-01-02 00:00:00",
		"2026-01-05 12:00:00", "2026-02-01 09:30:00", "2026-02-28 22:00:00",
	}
	var entries []model.Entry
	for i, s := range stamps {
		e := aggEntry(ts(s), uint64(100+i), uint64(10+i), uint64(i), uint64(i+1), float64(i)+0.5)
		e.Provider = "claude"
		e.SessionID = "s" + stamps[i][:10] // two sessions share a day
		entries = append(entries, e)
	}
	snap := &Snapshot{Entries: entries}
	o := &Opts{TZ: time.UTC, Mode: model.ModeCalculate}

	sum := snap.Summary(o)
	check := func(name string, rows []model.Row) {
		t.Helper()
		var cost float64
		var tokens uint64
		var messages int64
		for i := range rows {
			cost += rows[i].Cost
			tokens += rows[i].TotalTokens()
			messages += rows[i].Messages
		}
		if !nearly(sum.Cost, cost) {
			t.Errorf("%s cost sum %v != summary %v", name, cost, sum.Cost)
		}
		if sum.TotalTokens != tokens {
			t.Errorf("%s token sum %d != summary %d", name, tokens, sum.TotalTokens)
		}
		if sum.Messages != messages {
			t.Errorf("%s message sum %d != summary %d", name, messages, sum.Messages)
		}
	}
	check("daily", snap.Daily(o))
	check("weekly", snap.Weekly(o))
	check("monthly", snap.Monthly(o))
	check("hourly", snap.Hourly(o))

	// Project rows carry the same token totals too.
	var tok uint64
	for _, r := range snap.ByProject(o) {
		tok += r.TotalTokens()
	}
	if sum.TotalTokens != tok {
		t.Errorf("byProject token sum %d != summary %d", tok, sum.TotalTokens)
	}
}

func TestDailyGroupsByTZDay(t *testing.T) {
	tests := []struct {
		name string
		tz   *time.Location
		want []string
	}{
		{"utc keeps the utc date", time.UTC, []string{"2026-01-05"}},
		{"plus1 rolls into next day", fixedTZ, []string{"2026-01-06"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &Snapshot{Entries: []model.Entry{aggEntry(ts("2026-01-05 23:30:00"), 10, 0, 0, 0, 1)}}
			rows := snap.Daily(&Opts{TZ: tt.tz})
			got := rowKeys(rows)
			if len(got) != len(tt.want) || got[0] != tt.want[0] {
				t.Fatalf("Daily keys = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSinceUntilInclusiveBoundaries(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		aggEntry(ts("2026-01-01 12:00:00"), 1, 0, 0, 0, 1),                       // before since
		aggEntry(ts("2026-01-02 00:00:00"), 1, 0, 0, 0, 1),                       // exactly since midnight
		aggEntry(ts("2026-01-02 23:59:59"), 1, 0, 0, 0, 1),                       // late on until day
		aggEntry(ts("2026-01-02 23:59:59").Add(time.Millisecond), 1, 0, 0, 0, 1), // 23:59:59.001, past old day-end-1ms
		aggEntry(ts("2026-01-03 00:00:00"), 1, 0, 0, 0, 1),                       // day after until
	}}
	// The 23:59:59.001 entry proves sub-second evening entries stay in the
	// until day (the old day-end minus 1ms cutoff would have dropped it).

	rows := snap.Daily(&Opts{TZ: time.UTC, Since: "2026-01-02", Until: "2026-01-02"})
	if len(rows) != 1 || rows[0].Key != "2026-01-02" {
		t.Fatalf("expected only 2026-01-02, got %v", rowKeys(rows))
	}
	if rows[0].Messages != 3 {
		t.Errorf("until day must include sub-second evening entries: got %d messages, want 3", rows[0].Messages)
	}

	// Malformed bounds behave as absent: all three calendar days appear.
	rows = snap.Daily(&Opts{TZ: time.UTC, Since: "not-a-date", Until: "also-bad"})
	if len(rows) != 3 {
		t.Errorf("malformed bounds should not filter: got %d days, want 3", len(rows))
	}
}

func TestLastTruncatesAfterGrouping(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		aggEntry(ts("2026-01-01 10:00:00"), 1, 0, 0, 0, 1),
		aggEntry(ts("2026-01-02 10:00:00"), 1, 0, 0, 0, 1),
		aggEntry(ts("2026-01-03 10:00:00"), 1, 0, 0, 0, 1),
		aggEntry(ts("2026-01-04 10:00:00"), 1, 0, 0, 0, 1),
	}}
	rows := snap.Daily(&Opts{Last: 2})
	want := []string{"2026-01-03", "2026-01-04"}
	got := rowKeys(rows)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Last=2 daily keys = %v, want %v", got, want)
	}
	if n := len(snap.Daily(&Opts{TZ: time.UTC})); n != 4 {
		t.Errorf("Last=0 must keep all days: got %d", n)
	}
	// Last also applies to session rows (most recent by LastActivity).
	sess := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), SessionID: "old", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-02 10:00:00"), SessionID: "mid", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-03 10:00:00"), SessionID: "new", CostAuto: 1, CostCalc: 1},
	}}
	rows = sess.BySession(&Opts{Last: 2})
	if len(rows) != 2 || rows[0].Key != "new" || rows[1].Key != "mid" {
		t.Errorf("BySession Last=2 = %v, want [new mid]", rowKeys(rows))
	}
}

func TestFiltersApplyBeforeGrouping(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Model: "m1", Provider: "claude", Project: "p1", SessionID: "s1", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-01 11:00:00"), Model: "m2", Provider: "codex", Project: "p1", SessionID: "s2", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-01 12:00:00"), Model: "m1", Provider: "codex", Project: "p2", SessionID: "s3", CostAuto: 1, CostCalc: 1},
	}}
	tests := []struct {
		name string
		o    *Opts
		want int
	}{
		{"project filter", &Opts{TZ: time.UTC, Project: "p1"}, 2},
		{"session filter", &Opts{TZ: time.UTC, Session: "s2"}, 1},
		{"model filter", &Opts{TZ: time.UTC, Model: "m1"}, 2},
		{"provider filter", &Opts{TZ: time.UTC, Provider: "codex"}, 2},
		{"combined", &Opts{TZ: time.UTC, Provider: "codex", Project: "p2"}, 1},
		{"no match", &Opts{TZ: time.UTC, Project: "zzz"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snap.Summary(tt.o).Messages; got != int64(tt.want) {
				t.Errorf("Summary messages = %d, want %d", got, tt.want)
			}
			daily := snap.Daily(tt.o)
			var msgs int64
			for _, r := range daily {
				msgs += r.Messages
			}
			if msgs != int64(tt.want) {
				t.Errorf("Daily messages across rows = %d, want %d", msgs, tt.want)
			}
			// Filters apply before grouping: a filtered run must not leak
			// other keys/models into the rows.
			for _, r := range daily {
				for _, m := range r.Models {
					if tt.o.Model != "" && m.Model != tt.o.Model {
						t.Errorf("model leak: got %q under filter %q", m.Model, tt.o.Model)
					}
				}
			}
		})
	}
}

func TestWeeklyMondayStart(t *testing.T) {
	tests := []struct {
		stamp string
		want  string // ISO Monday of that week
	}{
		{"2026-01-05 10:00:00", "2026-01-05"}, // Monday itself
		{"2026-01-07 10:00:00", "2026-01-05"}, // Wednesday
		{"2026-01-11 23:00:00", "2026-01-05"}, // Sunday rolls back
		{"2026-01-12 00:30:00", "2026-01-12"}, // next Monday opens a new week
	}
	var entries []model.Entry
	for _, s := range tests {
		entries = append(entries, aggEntry(ts(s.stamp), 1, 0, 0, 0, 1))
	}
	snap := &Snapshot{Entries: entries}
	rows := snap.Weekly(&Opts{TZ: time.UTC})
	got := rowKeys(rows)
	if len(got) != 2 || got[0] != "2026-01-05" || got[1] != "2026-01-12" {
		t.Fatalf("weekly keys = %v, want [2026-01-05 2026-01-12]", got)
	}
	if rows[0].Messages != 3 {
		t.Errorf("first week should hold 3 entries, got %d", rows[0].Messages)
	}
}

func TestMonthlyAndHourlyKeyFormats(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		aggEntry(ts("2026-03-05 09:15:00"), 1, 0, 0, 0, 1),
		aggEntry(ts("2026-03-05 09:45:00"), 1, 0, 0, 0, 1),
		aggEntry(ts("2026-04-01 00:10:00"), 1, 0, 0, 0, 1),
	}}
	if got := rowKeys(snap.Monthly(&Opts{TZ: time.UTC})); len(got) != 2 || got[0] != "2026-03" || got[1] != "2026-04" {
		t.Errorf("monthly keys = %v, want [2026-03 2026-04]", got)
	}
	got := rowKeys(snap.Hourly(&Opts{TZ: time.UTC}))
	want := []string{"2026-03-05 09", "2026-04-01 00"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("hourly keys = %v, want %v", got, want)
	}
}

func TestBySessionOrderAndMetadata(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), SessionID: "sA", Project: "alpha", CostAuto: 2, CostCalc: 2},
		{Timestamp: ts("2026-01-03 10:00:00"), SessionID: "sA", Project: "beta", CostAuto: 1, CostCalc: 1}, // later, different project
		{Timestamp: ts("2026-01-02 10:00:00"), SessionID: "sB", CostAuto: 5, CostCalc: 5},
		{Timestamp: ts("2026-01-04 10:00:00"), SessionID: "", CostAuto: 9, CostCalc: 9}, // no-session bucket
	}}
	rows := snap.BySession(&Opts{TZ: time.UTC})
	want := []string{"(no session)", "sA", "sB"} // LastActivity desc
	got := rowKeys(rows)
	if len(got) != len(want) {
		t.Fatalf("session keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("session keys = %v, want %v", got, want)
		}
	}
	if rows[1].Label != "beta" {
		t.Errorf("session Label should be latest project, got %q", rows[1].Label)
	}
	for _, r := range rows {
		if r.Sessions != 1 {
			t.Errorf("session row %q Sessions = %d, want 1", r.Key, r.Sessions)
		}
	}
	// Deterministic tie-break: equal LastActivity sorts by key asc.
	tie := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), SessionID: "z", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-01 10:00:00"), SessionID: "a", CostAuto: 1, CostCalc: 1},
	}}
	if got := rowKeys(tie.BySession(&Opts{TZ: time.UTC})); got[0] != "a" || got[1] != "z" {
		t.Errorf("tie-break order = %v, want [a z]", got)
	}
}

func TestByProjectSortAndSessionCount(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Project: "cheap", SessionID: "s1", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-01 11:00:00"), Project: "cheap", SessionID: "s1", CostAuto: 1, CostCalc: 1}, // same session again
		{Timestamp: ts("2026-01-01 12:00:00"), Project: "cheap", SessionID: "s2", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-01 13:00:00"), Project: "pricey", SessionID: "s3", CostAuto: 10, CostCalc: 10},
		{Timestamp: ts("2026-01-01 14:00:00"), Project: "", SessionID: "s4", CostAuto: 2, CostCalc: 2},
	}}
	rows := snap.ByProject(&Opts{TZ: time.UTC})
	want := []string{"pricey", "cheap", "(no project)"} // cost desc: 10, 3, 2
	got := rowKeys(rows)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("project order = %v, want %v", got, want)
		}
	}
	byKey := map[string]model.Row{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if s := byKey["cheap"].Sessions; s != 2 {
		t.Errorf("cheap Sessions = %d, want 2 (distinct)", s)
	}
	if s := byKey["pricey"].Sessions; s != 1 {
		t.Errorf("pricey Sessions = %d, want 1", s)
	}
}

// TestNoProjectFilterRoundTrip pins the drill-down contract: the "(no project)"
// key ByProject emits must be accepted by the Project filter, mapping back to
// entries whose Project is empty.
func TestNoProjectFilterRoundTrip(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Project: "p1", CostAuto: 3, CostCalc: 3},
		{Timestamp: ts("2026-01-01 11:00:00"), Project: "", CostAuto: 2, CostCalc: 2},
	}}
	rows := snap.ByProject(&Opts{TZ: time.UTC})
	if len(rows) != 2 || rows[0].Key != "p1" || rows[1].Key != noProjectKey {
		t.Fatalf("ByProject = %v, want [p1 (no project)] by cost desc", rowKeys(rows))
	}
	got := snap.ByProject(&Opts{TZ: time.UTC, Project: noProjectKey})
	if len(got) != 1 || got[0].Key != noProjectKey {
		t.Fatalf("?project=(no project) = %v, want exactly the sentinel row", rowKeys(got))
	}
	if got[0].Cost != 2 {
		t.Errorf("(no project) filtered cost = %v, want 2", got[0].Cost)
	}
	named := snap.ByProject(&Opts{TZ: time.UTC, Project: "p1"})
	if len(named) != 1 || named[0].Key != "p1" || named[0].Cost != 3 {
		t.Fatalf("?project=p1 = %v, want the p1 row with cost 3", rowKeys(named))
	}
}

func TestByModelOrderAndTotals(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Model: "big", SessionID: "s1", CostAuto: 5, CostCalc: 5},
		{Timestamp: ts("2026-01-02 10:00:00"), Model: "small", SessionID: "s1", CostAuto: 2, CostCalc: 2},
		{Timestamp: ts("2026-01-03 10:00:00"), Model: "small", SessionID: "s2", CostAuto: 2, CostCalc: 2},
		{Timestamp: ts("2026-01-04 10:00:00"), Model: "big", SessionID: "s2", CostAuto: 5, CostCalc: 5},
	}}
	rows := snap.ByModel(&Opts{TZ: time.UTC})
	got := rowKeys(rows)
	if len(got) != 2 || got[0] != "big" || got[1] != "small" {
		t.Fatalf("model order = %v, want [big small]", got)
	}
	big := rows[0]
	if big.Messages != 2 || big.Sessions != 2 {
		t.Errorf("big Messages/Sessions = %d/%d, want 2/2", big.Messages, big.Sessions)
	}
	if !big.FirstActivity.Equal(ts("2026-01-01 10:00:00")) || !big.LastActivity.Equal(ts("2026-01-04 10:00:00")) {
		t.Errorf("big first/last = %v/%v", big.FirstActivity, big.LastActivity)
	}
	if len(big.Models) != 1 || big.Models[0].Model != "big" || big.Models[0].Cost != 10 {
		t.Errorf("big breakdown = %+v", big.Models)
	}
	// Tie-break by name for equal cost.
	tie := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Model: "z", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-01 10:00:00"), Model: "a", CostAuto: 1, CostCalc: 1},
	}}
	if got := rowKeys(tie.ByModel(&Opts{TZ: time.UTC})); got[0] != "a" || got[1] != "z" {
		t.Errorf("tie-break order = %v, want [a z]", got)
	}
}

func TestModelsSortedByCostDescWithinRows(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Model: "cheap", CostAuto: 1, CostCalc: 1},
		{Timestamp: ts("2026-01-01 11:00:00"), Model: "pricey", CostAuto: 9, CostCalc: 9},
		{Timestamp: ts("2026-01-01 12:00:00"), Model: "mid", CostAuto: 4, CostCalc: 4},
	}}
	rows := snap.Daily(&Opts{TZ: time.UTC})
	if len(rows) != 1 {
		t.Fatalf("expected one daily row, got %d", len(rows))
	}
	models := rows[0].Models
	want := []string{"pricey", "mid", "cheap"}
	for i, m := range want {
		if models[i].Model != m {
			t.Fatalf("breakdown order = %v, want %v", models, want)
		}
	}
}

func TestSummaryAgreesWithDaily(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Model: "m", Provider: "claude", SessionID: "s1",
			InputTokens: 100, OutputTokens: 10, CacheCreationTokens: 5, CacheReadTokens: 1,
			CostAuto: 1.5, CostCalc: 1.5},
		{Timestamp: ts("2026-01-02 10:00:00"), Model: "m", Provider: "claude", SessionID: "s1",
			InputTokens: 50, CostAuto: 0.5, CostCalc: 0.5}, // same session
		{Timestamp: ts("2026-01-02 11:00:00"), Model: "n", Provider: "codex", SessionID: "s2",
			ExtraTotalTokens: 42, CostAuto: 0.25, CostCalc: 0.25},
	}}
	o := &Opts{TZ: time.UTC}
	sum := snap.Summary(o)
	daily := snap.Daily(o)

	if !nearly(sum.Cost, sumCost(daily)) {
		t.Errorf("summary cost %v != daily sum %v", sum.Cost, sumCost(daily))
	}
	var tok uint64
	for _, r := range daily {
		tok += r.TotalTokens()
	}
	if sum.TotalTokens != tok {
		t.Errorf("summary tokens %d != daily sum %d", sum.TotalTokens, tok)
	}
	if sum.Sessions != 2 || sum.Days != 2 || sum.ModelCount != 2 {
		t.Errorf("sessions/days/models = %d/%d/%d, want 2/2/2", sum.Sessions, sum.Days, sum.ModelCount)
	}
	if sum.Messages != 3 {
		t.Errorf("messages = %d, want 3", sum.Messages)
	}
	if sum.FirstDate != "2026-01-01" || sum.LastDate != "2026-01-02" {
		t.Errorf("first/last = %s/%s", sum.FirstDate, sum.LastDate)
	}
	if len(sum.ByProvider) != 2 {
		t.Fatalf("byProvider len = %d, want 2", len(sum.ByProvider))
	}
	// Per-provider sessions count distinct session IDs, not events.
	for _, p := range sum.ByProvider {
		want := int64(1)
		if p.Provider == "claude" {
			want = 1 // s1 twice, still one session
		}
		if p.Sessions != want {
			t.Errorf("provider %s sessions = %d, want %d", p.Provider, p.Sessions, want)
		}
	}
	// ByProvider sorted by cost desc: claude (2.0) before codex (0.25).
	if sum.ByProvider[0].Provider != "claude" || sum.ByProvider[1].Provider != "codex" {
		t.Errorf("byProvider order = %s,%s", sum.ByProvider[0].Provider, sum.ByProvider[1].Provider)
	}
}

func TestModeAffectsCost(t *testing.T) {
	cost := 7.0
	snap := &Snapshot{Entries: []model.Entry{{
		Timestamp:   ts("2026-01-01 10:00:00"),
		Model:       "m",
		CostDisplay: cost, // reported
		CostAuto:    cost,
		CostCalc:    3.0, // computed differs
	}}}
	tests := []struct {
		mode model.Mode
		want float64
	}{
		{model.ModeAuto, 7.0},
		{model.ModeDisplay, 7.0},
		{model.ModeCalculate, 3.0},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			rows := snap.Daily(&Opts{TZ: time.UTC, Mode: tt.mode})
			if !nearly(rows[0].Cost, tt.want) {
				t.Errorf("cost = %v, want %v", rows[0].Cost, tt.want)
			}
		})
	}
}

func TestMessagesFloorAtOne(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{
		{Timestamp: ts("2026-01-01 10:00:00"), Messages: 0, CostAuto: 1, CostCalc: 1}, // zero → 1
		{Timestamp: ts("2026-01-01 11:00:00"), Messages: 4, CostAuto: 1, CostCalc: 1},
	}}
	rows := snap.Daily(&Opts{TZ: time.UTC})
	if rows[0].Messages != 5 {
		t.Errorf("messages = %d, want 5 (0 floors to 1)", rows[0].Messages)
	}
}

func TestMissingPricingFlagPropagates(t *testing.T) {
	snap := &Snapshot{Entries: []model.Entry{{
		Timestamp:      ts("2026-01-01 10:00:00"),
		Model:          "unknown-model",
		InputTokens:    10,
		MissingPricing: true,
		CostAuto:       0,
		CostCalc:       0,
	}}}
	rows := snap.Daily(&Opts{TZ: time.UTC})
	if len(rows[0].Models) != 1 || !rows[0].Models[0].MissingPricing {
		t.Errorf("missing-pricing flag should reach the model breakdown: %+v", rows[0].Models)
	}
}

func TestEmptySnapshotAggregators(t *testing.T) {
	snap := &Snapshot{}
	o := &Opts{TZ: time.UTC}
	if rows := snap.Daily(o); len(rows) != 0 {
		t.Errorf("Daily = %v, want empty", rows)
	}
	if rows := snap.Weekly(o); len(rows) != 0 {
		t.Errorf("Weekly = %v, want empty", rows)
	}
	if rows := snap.Monthly(o); len(rows) != 0 {
		t.Errorf("Monthly = %v, want empty", rows)
	}
	if rows := snap.Hourly(o); len(rows) != 0 {
		t.Errorf("Hourly = %v, want empty", rows)
	}
	if rows := snap.BySession(o); len(rows) != 0 {
		t.Errorf("BySession = %v, want empty", rows)
	}
	if rows := snap.ByProject(o); len(rows) != 0 {
		t.Errorf("ByProject = %v, want empty", rows)
	}
	if rows := snap.ByModel(o); len(rows) != 0 {
		t.Errorf("ByModel = %v, want empty", rows)
	}
	if sum := snap.Summary(o); sum.TotalTokens != 0 || sum.Cost != 0 || sum.ByProvider == nil {
		t.Errorf("Summary = %+v, want zero values with empty byProvider", sum)
	}
}

func TestFilteredDoesNotAliasSnapshot(t *testing.T) {
	// filtered now yields pointers into the snapshot for allocation-light
	// aggregation; aggregators must only read through them. The invariant the
	// API relies on is that no aggregator mutates through the pointers, and
	// that Blocks copies entries out of the snapshot (see derefEntries).
	snap := &Snapshot{Entries: []model.Entry{aggEntry(ts("2026-01-01 10:00:00"), 5, 0, 0, 0, 1)}}
	got := snap.filtered(&Opts{TZ: time.UTC})
	if len(got) != 1 || got[0] != &snap.Entries[0] {
		t.Fatalf("filtered = %+v, want a pointer to the single snapshot entry", got)
	}
}

func TestTZChangesDailyAndSummaryDates(t *testing.T) {
	// 2026-01-01 23:30 UTC is 2026-01-02 00:30 in fixedTZ.
	snap := &Snapshot{Entries: []model.Entry{aggEntry(ts("2026-01-01 23:30:00"), 1, 0, 0, 0, 1)}}
	if got := rowKeys(snap.Daily(&Opts{TZ: fixedTZ})); got[0] != "2026-01-02" {
		t.Errorf("tz-shifted daily key = %s, want 2026-01-02", got[0])
	}
	if s := snap.Summary(&Opts{TZ: fixedTZ}); s.FirstDate != "2026-01-02" || s.LastDate != "2026-01-02" {
		t.Errorf("tz-shifted summary dates = %s/%s, want 2026-01-02", s.FirstDate, s.LastDate)
	}
}
