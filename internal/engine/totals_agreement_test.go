package engine

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"xusage/internal/fixtures"
	"xusage/internal/model"
	"xusage/internal/pricing"
	claude "xusage/internal/sources/claude"
)

// walkClaudeFixtures lists the corpus's claude transcript files.
func walkClaudeFixtures(root string) ([]string, error) {
	return filepath.Glob(filepath.Join(root, "claude", "projects", "*", "*.jsonl"))
}

// TestTotalsAgreeAcrossAggregators runs the seeded fixtures corpus through a
// real load (discover → parse → dedup → price) and asserts daily, weekly,
// monthly, hourly, session, project, and model rows all sum to the same grand
// totals as Summary. SPEC §6: every aggregator shares one accumulator.
func TestTotalsAgreeAcrossAggregators(t *testing.T) {
	registerStubSource(t) // keep the registry deterministic; corpus loads via snapshot below

	dir := t.TempDir()
	fixtures.WriteCorpus(dir, fixtures.CorpusOpts{WithErrors: true})

	// Parse the corpus through the REAL claude adapter so pricing/dedup run.
	var entries []model.Entry
	paths, err := walkClaudeFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("corpus produced no files")
	}
	for _, p := range paths {
		ents, err := claude.Parse(p)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		entries = append(entries, ents...)
	}
	for i := range entries {
		entries[i].Provider = "claude"
	}
	entries = dedupe(entries)
	price(entries)
	snap := &Snapshot{Entries: entries, BuiltAt: time.Now()}

	o := &Opts{TZ: time.UTC, Mode: model.ModeCalculate} // computed costs exercise the pricing path
	sum := snap.Summary(o)

	type totals struct {
		cost     float64
		tokens   uint64
		messages int64
	}
	check := func(name string, rows []model.Row, want totals) {
		t.Helper()
		var got totals
		var tok uint64
		for i := range rows {
			got.cost += rows[i].Cost
			got.messages += rows[i].Messages
			tok += rows[i].TotalTokens()
		}
		got.tokens = tok
		if math.Abs(got.cost-want.cost) > 1e-6*math.Max(1, want.cost) {
			t.Errorf("%s cost = %v, want %v", name, got.cost, want.cost)
		}
		if got.tokens != want.tokens {
			t.Errorf("%s tokens = %d, want %d", name, got.tokens, want.tokens)
		}
		if got.messages != want.messages {
			t.Errorf("%s messages = %d, want %d", name, got.messages, want.messages)
		}
	}
	want := totals{
		cost:     sum.Cost,
		tokens:   sum.TotalTokens,
		messages: sum.Messages,
	}
	if want.tokens == 0 {
		t.Fatal("fixture totals are zero; corpus did not load")
	}

	check("daily", snap.Daily(o), want)
	check("weekly", snap.Weekly(o), want)
	check("monthly", snap.Monthly(o), want)
	check("hourly", snap.Hourly(o), want)
	check("sessions", snap.BySession(o), want)
	check("projects", snap.ByProject(o), want)
	check("models", snap.ByModel(o), want)

	// Weekly must also agree with the daily rows it groups (weekly is a
	// coarsening of daily, never an independent total).
	var weeklyFromDaily float64
	daily := snap.Daily(o)
	weekOf := func(day string) string {
		d, _ := time.ParseInLocation("2006-01-02", day, time.UTC)
		wd := (int(d.Weekday()) + 6) % 7
		return d.AddDate(0, 0, -wd).Format("2006-01-02")
	}
	perWeek := map[string]float64{}
	for _, r := range daily {
		perWeek[weekOf(r.Key)] += r.Cost
	}
	weekly := snap.Weekly(o)
	if len(weekly) != len(perWeek) {
		t.Fatalf("weekly rows = %d, daily-derived weeks = %d", len(weekly), len(perWeek))
	}
	for _, r := range weekly {
		weeklyFromDaily += perWeek[r.Key]
	}
	if math.Abs(weeklyFromDaily-sum.Cost) > 1e-6*math.Max(1, sum.Cost) {
		t.Errorf("weekly keys do not cover daily: %v vs %v", weeklyFromDaily, sum.Cost)
	}

	// Per-provider split must reconcile with the grand total too.
	var providerCost float64
	for _, p := range sum.ByProvider {
		providerCost += p.Cost
	}
	if math.Abs(providerCost-sum.Cost) > 1e-6*math.Max(1, sum.Cost) {
		t.Errorf("byProvider cost %v != summary cost %v", providerCost, sum.Cost)
	}
}

// TestModeDisplayVsCalculateDifferOnCorpus pins that the fixture corpus mixes
// reported and unreported costs, so display and calculate modes genuinely
// diverge on the same data (guards against a regression to always-reported).
func TestModeDisplayVsCalculateDifferOnCorpus(t *testing.T) {
	dir := t.TempDir()
	fixtures.WriteCorpus(dir, fixtures.CorpusOpts{})

	var entries []model.Entry
	paths, err := walkClaudeFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		ents, err := claude.Parse(p)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, ents...)
	}
	for i := range entries {
		entries[i].Provider = "claude"
	}
	entries = dedupe(entries)
	price(entries)
	snap := &Snapshot{Entries: entries}

	display := snap.Summary(&Opts{TZ: time.UTC, Mode: model.ModeDisplay}).Cost
	calculate := snap.Summary(&Opts{TZ: time.UTC, Mode: model.ModeCalculate}).Cost
	auto := snap.Summary(&Opts{TZ: time.UTC, Mode: model.ModeAuto}).Cost

	if display <= 0 || calculate <= 0 {
		t.Fatalf("display=%v calculate=%v, both must be positive on the corpus", display, calculate)
	}
	if math.Abs(display-calculate) < 1e-9 {
		t.Error("display and calculate costs identical; corpus lost its unreported-cost entries")
	}
	if auto <= display {
		t.Errorf("auto (%v) should exceed display (%v) when some entries lack reported cost", auto, display)
	}
	_ = pricing.Get() // pricing table warm; price() used it above
}
