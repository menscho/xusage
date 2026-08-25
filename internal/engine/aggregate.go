// Package engine — aggregation of priced entries into report rows.
//
// All aggregators share one accumulator so daily/weekly/monthly/hourly,
// session, project, and model reports always agree with each other. Filters
// apply to entries before grouping; Last truncates after grouping+sorting.
package engine

import (
	"sort"
	"time"

	"xusage/internal/model"
)

// Opts carries the shared filter/group options for every aggregator.
// Zero-value Opts means: local timezone, no date range, no filters.
type Opts struct {
	TZ       *time.Location // nil = time.Local
	Since    string         // YYYY-MM-DD inclusive, empty = unbounded
	Until    string         // YYYY-MM-DD inclusive, empty = unbounded
	Last     int            // keep only the n most recent groups (0 = all)
	Project  string         // exact match, empty = no filter
	Session  string         // exact match, empty = no filter
	Model    string         // exact match on display model, empty = no filter
	Provider string         // exact match, empty = no filter
	Vendor   string         // exact VendorOf(Model) match, empty = no filter
	Mode     model.Mode
}

// noProjectKey is the display key ByProject groups unattributed entries
// under; the project filter maps it back to empty Entry.Project values so
// drill-down round-trips (row key → ?project= filter) keep working.
const noProjectKey = "(no project)"

func (o *Opts) loc() *time.Location {
	if o.TZ != nil {
		return o.TZ
	}
	return time.Local
}

// match reports whether entry e passes the filter options o within the
// precomputed [since, until) bounds (in loc). Kept as one shared predicate so
// filtered() and the inline aggregator loops below never diverge on filter
// rules. Returns false on the first failing clause, so the common no-filter
// path does only the two timestamp checks per entry.
func match(o *Opts, loc *time.Location, since, until time.Time, e *model.Entry) bool {
	if o.Project != "" {
		p := e.Project
		if p == "" {
			p = noProjectKey // ByProject groups unattributed entries under this key
		}
		if p != o.Project {
			return false
		}
	}
	if o.Session != "" && e.SessionID != o.Session {
		return false
	}
	if o.Model != "" && e.Model != o.Model {
		return false
	}
	if o.Provider != "" && e.Provider != o.Provider {
		return false
	}
	if o.Vendor != "" && VendorOf(e.Model) != o.Vendor {
		return false
	}
	if !since.IsZero() && e.Timestamp.Before(since) {
		return false
	}
	if !until.IsZero() && !e.Timestamp.Before(until) {
		return false
	}
	return true
}

// filtered returns the snapshot entries passing the Opts filters, in order.
// It yields pointers into the snapshot instead of copying whole Entry
// structs: aggregators only ever read through these pointers and never sort
// or mutate the snapshot's backing array, so no aliasing hazard exists.
// Today only tests materialize the full match set through this helper: every
// production aggregator (daily…summary, ByVendor, blocks) iterates s.Entries
// inline through match() to skip the intermediate slice — a []*model.Entry
// sized to the whole snapshot was the largest per-request allocation on the
// row endpoints (~8 B/entry), which is why the inline path is the default.
func (s *Snapshot) filtered(o *Opts) []*model.Entry {
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	out := make([]*model.Entry, 0, len(s.Entries))
	for i := range s.Entries {
		e := &s.Entries[i]
		if match(o, loc, since, until, e) {
			out = append(out, e)
		}
	}
	return out
}

func parseDay(day string, loc *time.Location) time.Time {
	if day == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseDayEndExclusive returns midnight of the day after day in loc: an entry
// belongs to the range when its timestamp is Before this instant, which keeps
// the whole until day inclusive at any timestamp precision (the previous
// day-end minus 1ms dropped sub-millisecond evening entries). A malformed or
// empty day yields zero time = no upper bound.
func parseDayEndExclusive(day string, loc *time.Location) time.Time {
	t := parseDay(day, loc)
	if t.IsZero() {
		return t
	}
	return t.AddDate(0, 0, 1)
}

// acc accumulates entries into one Row plus per-model breakdowns.
type acc struct {
	row   model.Row
	model map[string]*model.ModelBreakdown
	order []string
}

func (a *acc) add(e *model.Entry, mode model.Mode) {
	a.row.InputTokens += e.InputTokens
	a.row.OutputTokens += e.OutputTokens
	a.row.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
	a.row.CacheReadTokens += e.CacheReadTokens
	a.row.ExtraTotalTokens += e.ExtraTotalTokens
	a.row.Cost += e.Cost(mode)
	a.row.Messages += int64(max(e.Messages, 1))
	if a.row.FirstActivity.IsZero() || e.Timestamp.Before(a.row.FirstActivity) {
		a.row.FirstActivity = e.Timestamp
	}
	if e.Timestamp.After(a.row.LastActivity) {
		a.row.LastActivity = e.Timestamp
	}
	b, ok := a.model[e.Model]
	if !ok {
		b = &model.ModelBreakdown{Model: e.Model}
		a.model[e.Model] = b
		a.order = append(a.order, e.Model)
	}
	b.InputTokens += e.InputTokens
	b.OutputTokens += e.OutputTokens
	b.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
	b.CacheReadTokens += e.CacheReadTokens
	b.ExtraTotalTokens += e.ExtraTotalTokens
	b.Cost += e.Cost(mode)
	if e.MissingPricing && b.TotalTokens() > 0 {
		b.MarkMissingPricing()
	}
}

// finish returns the accumulated row without per-model breakdowns; callers
// attach them separately via modelsSorted.
func (a *acc) finish() model.Row {
	return a.row
}

func newAcc() *acc { return &acc{model: map[string]*model.ModelBreakdown{}} }

// modelsSorted returns the per-model breakdowns sorted by cost descending,
// stable so equal-cost models keep first-seen order.
func (a *acc) modelsSorted() []model.ModelBreakdown {
	out := make([]model.ModelBreakdown, 0, len(a.model))
	for _, name := range a.order {
		out = append(out, *a.model[name])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	return out
}

// sessionSets counts distinct sessions per group key K in one of two modes:
// interned Entry.SessionIdx integers on engine-built snapshots (avoids hashing
// long ID strings per entry — SPEC §10 warm budget), or raw SessionID strings
// otherwise (hand-built test snapshots carry SessionIdx == 0).
type sessionSets[K comparable] struct {
	idx map[K]map[int]bool
	str map[K]map[string]bool
}

// newSessionSets picks the accounting mode for a snapshot up front.
func newSessionSets[K comparable](interned bool) sessionSets[K] {
	if interned {
		return sessionSets[K]{idx: map[K]map[int]bool{}}
	}
	return sessionSets[K]{str: map[K]map[string]bool{}}
}

// add records e's session under group k (no-op for session-less entries).
func (ss *sessionSets[K]) add(k K, e *model.Entry) {
	if ss.idx != nil {
		if e.SessionIdx == 0 {
			return
		}
		set := ss.idx[k]
		if set == nil {
			set = map[int]bool{}
			ss.idx[k] = set
		}
		set[e.SessionIdx] = true
		return
	}
	if e.SessionID == "" {
		return
	}
	set := ss.str[k]
	if set == nil {
		set = map[string]bool{}
		ss.str[k] = set
	}
	set[e.SessionID] = true
}

// count reports the number of distinct sessions recorded under k.
func (ss *sessionSets[K]) count(k K) int64 {
	if ss.idx != nil {
		return int64(len(ss.idx[k]))
	}
	return int64(len(ss.str[k]))
}

// sessionsInterned reports whether Entries carry interned session indexes:
// decided from the first entry with a non-empty SessionID. Engine-built
// snapshots always intern (loadAll/NewSnapshotForTest), so the check is
// exact for them; hand-built snapshots yield false and take the slow path.
func (s *Snapshot) sessionsInterned() bool {
	for i := range s.Entries {
		if s.Entries[i].SessionID != "" {
			return s.Entries[i].SessionIdx > 0
		}
	}
	return false // no session IDs anywhere; the mode never matters
}

// groupBy groups filtered entries by an integer period bucket (see the
// dayIndex/weekIndex/monthIndex/hourIndex helpers), producing rows sorted
// ascending by bucket — the same order as the previous formatted-string sort,
// because the label layouts are fixed-width and monotonic in the bucket.
// Grouping on integers instead of per-entry Format + string-map keys keeps
// warm aggregation off the hashing/formatting hot path (SPEC §10 budget);
// each display key is rendered exactly once per group. Sessions counts
// distinct SessionIDs per group. Last truncates to the n most recent
// (highest-bucket) groups after sorting.
func (s *Snapshot) groupBy(o *Opts,
	bucket func(*model.Entry, *time.Location) int64, label func(int64) string,
) []model.Row {
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	groups := map[int64]*acc{}
	sets := newSessionSets[int64](s.sessionsInterned())
	var keys []int64
	for i := range s.Entries {
		e := &s.Entries[i]
		if !match(o, loc, since, until, e) {
			continue
		}
		k := bucket(e, loc)
		a, ok := groups[k]
		if !ok {
			a = newAcc()
			groups[k] = a
			keys = append(keys, k)
		}
		a.add(e, o.Mode)
		sets.add(k, e)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	rows := make([]model.Row, 0, len(keys))
	for _, k := range keys {
		r := groups[k].finish()
		r.Key = label(k)
		r.Sessions = sets.count(k)
		r.Models = groups[k].modelsSorted()
		rows = append(rows, r)
	}
	if o.Last > 0 && len(rows) > o.Last {
		rows = rows[len(rows)-o.Last:]
	}
	return rows
}

// civilDayIndex returns days since 1970-01-01 for the calendar date y-m-d
// (Howard Hinnant's days_from_civil algorithm; valid for the proleptic
// Gregorian calendar, which is what time.Time civil dates use).
func civilDayIndex(y, m, d int) int64 {
	yy := int64(y)
	if m <= 2 {
		yy--
	}
	era := floorDiv(yy, 400)
	yoe := yy - era*400 // [0, 399]
	mp := int64(m)
	if mp > 2 {
		mp -= 3
	} else {
		mp += 9
	}
	doy := (153*mp+2)/5 + int64(d) - 1     // [0, 365]
	doe := yoe*365 + yoe/4 - yoe/100 + doy // [0, 146096]
	return era*146097 + doe - 719468
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// floorMod is the sign-correct companion of floorDiv.
func floorMod(a, b int64) int64 { return a - floorDiv(a, b)*b }

// Period buckets: monotonic integer keys derived from the entry's localized
// wall clock (DST-safe — civil fields are read from the located time).

// dayBucket indexes the calendar day of e in loc.
func dayBucket(t time.Time) int64 {
	y, m, d := t.Date()
	return civilDayIndex(y, int(m), d)
}

// weekBucket indexes the Monday-start week containing t's day.
// Day 0 (1970-01-01) was a Thursday, giving Monday index (d+3)%7.
func weekBucket(t time.Time) int64 {
	d := dayBucket(t)
	return d - floorMod(d+3, 7)
}

// monthBucket indexes the calendar month as y*12+(m-1).
func monthBucket(t time.Time) int64 {
	y, m, _ := t.Date()
	return int64(y)*12 + int64(m) - 1
}

// hourBucket indexes the day-hour pair as day*24+hour.
func hourBucket(t time.Time) int64 {
	return dayBucket(t)*24 + int64(t.Hour())
}

// Period labels: rendered once per group; byte-identical to the layouts the
// aggregators printed before the integer-bucket switch.

func dayLabel(k int64) string {
	return time.Unix(k*86400, 0).UTC().Format("2006-01-02")
}

func weekLabel(k int64) string {
	return time.Unix(k*86400, 0).UTC().Format("2006-01-02")
}

func hourLabel(k int64) string {
	return time.Unix((k/24)*86400+k%24*3600, 0).UTC().Format("2006-01-02 15")
}

func monthLabel(k int64) string {
	y := floorDiv(k, 12)
	m := k - y*12 + 1
	return time.Date(int(y), time.Month(m), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
}

// Daily aggregates by calendar day in the option timezone.
func (s *Snapshot) Daily(o *Opts) []model.Row {
	return s.groupBy(o, func(e *model.Entry, loc *time.Location) int64 {
		return dayBucket(e.Timestamp.In(loc))
	}, dayLabel)
}

// Weekly aggregates by Monday-start week; Key is the week's Monday date.
func (s *Snapshot) Weekly(o *Opts) []model.Row {
	return s.groupBy(o, func(e *model.Entry, loc *time.Location) int64 {
		return weekBucket(e.Timestamp.In(loc))
	}, weekLabel)
}

// Monthly aggregates by calendar month; Key is "2006-01".
func (s *Snapshot) Monthly(o *Opts) []model.Row {
	return s.groupBy(o, func(e *model.Entry, loc *time.Location) int64 {
		return monthBucket(e.Timestamp.In(loc))
	}, monthLabel)
}

// Hourly aggregates by hour; Key is "2006-01-02 15" (24×N grid for heatmaps).
func (s *Snapshot) Hourly(o *Opts) []model.Row {
	return s.groupBy(o, func(e *model.Entry, loc *time.Location) int64 {
		return hourBucket(e.Timestamp.In(loc))
	}, hourLabel)
}

// BySession aggregates per session, sorted by LastActivity descending (ties
// broken by session ID for determinism). Label carries the project of the
// session's most recent entry.
func (s *Snapshot) BySession(o *Opts) []model.Row {
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	type meta struct {
		project string
		at      time.Time
	}
	groups := map[string]*acc{}
	latest := map[string]meta{}
	for i := range s.Entries {
		e := &s.Entries[i]
		if !match(o, loc, since, until, e) {
			continue
		}
		id := e.SessionID
		if id == "" {
			id = "(no session)"
		}
		a := groups[id]
		if a == nil {
			a = newAcc()
			groups[id] = a
		}
		a.add(e, o.Mode)
		m := latest[id]
		if e.Timestamp.After(m.at) {
			latest[id] = meta{project: e.Project, at: e.Timestamp}
		}
	}
	rows := make([]model.Row, 0, len(groups))
	for id, a := range groups {
		r := a.finish()
		r.Key = id
		r.Label = latest[id].project
		r.Models = a.modelsSorted()
		r.Sessions = 1
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].LastActivity.Equal(rows[j].LastActivity) {
			return rows[i].LastActivity.After(rows[j].LastActivity)
		}
		return rows[i].Key < rows[j].Key
	})
	if o.Last > 0 && len(rows) > o.Last {
		rows = rows[:o.Last]
	}
	return rows
}

// ByProject aggregates per project, sorted by cost descending (ties broken by
// project name). Sessions counts distinct session IDs per project.
func (s *Snapshot) ByProject(o *Opts) []model.Row {
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	groups := map[string]*acc{}
	sets := newSessionSets[string](s.sessionsInterned())
	for i := range s.Entries {
		e := &s.Entries[i]
		if !match(o, loc, since, until, e) {
			continue
		}
		p := e.Project
		if p == "" {
			p = noProjectKey
		}
		a := groups[p]
		if a == nil {
			a = newAcc()
			groups[p] = a
		}
		a.add(e, o.Mode)
		sets.add(p, e)
	}
	rows := make([]model.Row, 0, len(groups))
	for p, a := range groups {
		r := a.finish()
		r.Key = p
		r.Models = a.modelsSorted()
		r.Sessions = sets.count(p)
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cost != rows[j].Cost {
			return rows[i].Cost > rows[j].Cost
		}
		return rows[i].Key < rows[j].Key
	})
	if o.Last > 0 && len(rows) > o.Last {
		rows = rows[:o.Last]
	}
	return rows
}

// ByModel aggregates global per-model totals, sorted by cost descending (ties
// broken by model name). Each row embeds its own single-model breakdown so all
// row endpoints share one shape; per-model cost share is derivable client-side
// from Cost against the set of rows.
func (s *Snapshot) ByModel(o *Opts) []model.Row {
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	interned := s.sessionsInterned()
	type m struct {
		b         model.ModelBreakdown
		firstSeen time.Time
		lastSeen  time.Time
		sessions  map[int]bool // interned SessionIdx (or SessionID hashes below)
		sessStr   map[string]bool
		messages  int64
	}
	groups := map[string]*m{}
	for i := range s.Entries {
		e := &s.Entries[i]
		if !match(o, loc, since, until, e) {
			continue
		}
		g := groups[e.Model]
		if g == nil {
			g = &m{}
			groups[e.Model] = g
		}
		g.b.Add(e, o.Mode)
		if e.MissingPricing {
			g.b.MarkMissingPricing()
		}
		if g.firstSeen.IsZero() || e.Timestamp.Before(g.firstSeen) {
			g.firstSeen = e.Timestamp
		}
		if e.Timestamp.After(g.lastSeen) {
			g.lastSeen = e.Timestamp
		}
		if interned {
			if e.SessionIdx != 0 {
				if g.sessions == nil {
					g.sessions = map[int]bool{}
				}
				g.sessions[e.SessionIdx] = true
			}
		} else if e.SessionID != "" {
			if g.sessStr == nil {
				g.sessStr = map[string]bool{}
			}
			g.sessStr[e.SessionID] = true
		}
		g.messages += int64(max(e.Messages, 1))
	}
	rows := make([]model.Row, 0, len(groups))
	for name, g := range groups {
		b := g.b
		sessions := len(g.sessions)
		if sessions == 0 && len(g.sessStr) > 0 {
			sessions = len(g.sessStr)
		}
		rows = append(rows, model.Row{
			Key:                 name,
			Label:               name,
			InputTokens:         b.InputTokens,
			OutputTokens:        b.OutputTokens,
			CacheCreationTokens: b.CacheCreationTokens,
			CacheReadTokens:     b.CacheReadTokens,
			ExtraTotalTokens:    b.ExtraTotalTokens,
			Cost:                b.Cost,
			Messages:            g.messages,
			Sessions:            int64(sessions),
			Models:              []model.ModelBreakdown{b},
			FirstActivity:       g.firstSeen,
			LastActivity:        g.lastSeen,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cost != rows[j].Cost {
			return rows[i].Cost > rows[j].Cost
		}
		return rows[i].Key < rows[j].Key
	})
	if o.Last > 0 && len(rows) > o.Last {
		rows = rows[:o.Last]
	}
	return rows
}

// Summary is the grand-total rollup shown at the top of the dashboard.
type Summary struct {
	InputTokens         uint64              `json:"inputTokens"`
	OutputTokens        uint64              `json:"outputTokens"`
	CacheCreationTokens uint64              `json:"cacheCreationTokens"`
	CacheReadTokens     uint64              `json:"cacheReadTokens"`
	ExtraTotalTokens    uint64              `json:"extraTotalTokens,omitempty"`
	TotalTokens         uint64              `json:"totalTokens"`
	Cost                float64             `json:"cost"`
	Messages            int64               `json:"messages"`
	Sessions            int64               `json:"sessions"`
	Days                int64               `json:"days"`
	FirstDate           string              `json:"firstDate,omitempty"`
	LastDate            string              `json:"lastDate,omitempty"`
	ModelCount          int64               `json:"modelCount"`
	ByProvider          []ProviderBreakdown `json:"byProvider"`
}

// ProviderBreakdown splits grand totals per source.
type ProviderBreakdown struct {
	Provider string  `json:"provider"`
	Cost     float64 `json:"cost"`
	TotalTok uint64  `json:"totalTokens"`
	Messages int64   `json:"messages"`
	Sessions int64   `json:"sessions"`
}

// Summary computes the grand totals over filtered entries. Per-provider
// sessions count distinct session IDs, not raw events.
func (s *Snapshot) Summary(o *Opts) Summary {
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	interned := s.sessionsInterned()
	var sum Summary
	// Only one of each pair below is ever written (gated by interned in the
	// loop); lazily allocating that one avoids a dead empty-map allocation on
	// the other branch per Summary call. len on the untouched nil map is 0, so
	// the totals computed below stay correct without the pre-sized empty maps.
	var sessionsIdx map[int]bool
	var sessionsStr map[string]bool
	if interned {
		sessionsIdx = map[int]bool{}
	} else {
		sessionsStr = map[string]bool{}
	}
	perProvider := map[string]*ProviderBreakdown{}
	var providerSessions map[string]map[int]bool
	var providerSessionsStr map[string]map[string]bool
	if interned {
		providerSessions = map[string]map[int]bool{}
	} else {
		providerSessionsStr = map[string]map[string]bool{}
	}
	models := map[string]bool{}
	days := map[int64]bool{} // civil-day buckets, labels rendered once below
	var dayCount int64
	var firstDay, lastDay int64
	for i := range s.Entries {
		e := &s.Entries[i]
		if !match(o, loc, since, until, e) {
			continue
		}
		sum.InputTokens += e.InputTokens
		sum.OutputTokens += e.OutputTokens
		sum.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
		sum.CacheReadTokens += e.CacheReadTokens
		sum.ExtraTotalTokens += e.ExtraTotalTokens
		sum.Cost += e.Cost(o.Mode)
		sum.Messages += int64(max(e.Messages, 1))
		if interned {
			if e.SessionIdx != 0 {
				sessionsIdx[e.SessionIdx] = true
			}
		} else if e.SessionID != "" {
			sessionsStr[e.SessionID] = true
		}
		models[e.Model] = true
		day := dayBucket(e.Timestamp.In(loc))
		if !days[day] {
			days[day] = true
			dayCount++
			if dayCount == 1 || day < firstDay {
				firstDay = day
			}
			if dayCount == 1 || day > lastDay {
				lastDay = day
			}
		}
		p := perProvider[e.Provider]
		if p == nil {
			p = &ProviderBreakdown{Provider: e.Provider}
			perProvider[e.Provider] = p
		}
		p.Cost += e.Cost(o.Mode)
		p.TotalTok += e.TotalTokens()
		p.Messages += int64(max(e.Messages, 1))
		if e.SessionID != "" {
			if interned {
				set := providerSessions[e.Provider]
				if set == nil {
					set = map[int]bool{}
					providerSessions[e.Provider] = set
				}
				set[e.SessionIdx] = true
			} else {
				set := providerSessionsStr[e.Provider]
				if set == nil {
					set = map[string]bool{}
					providerSessionsStr[e.Provider] = set
				}
				set[e.SessionID] = true
			}
		}
	}
	sum.TotalTokens = sum.InputTokens + sum.OutputTokens + sum.CacheCreationTokens +
		sum.CacheReadTokens + sum.ExtraTotalTokens
	sum.Sessions = int64(len(sessionsIdx) + len(sessionsStr))
	sum.Days = dayCount
	if dayCount > 0 {
		sum.FirstDate = dayLabel(firstDay)
		sum.LastDate = dayLabel(lastDay)
	}
	sum.ModelCount = int64(len(models))
	sum.ByProvider = make([]ProviderBreakdown, 0, len(perProvider))
	for _, p := range perProvider {
		p.Sessions = int64(len(providerSessions[p.Provider]) + len(providerSessionsStr[p.Provider]))
		sum.ByProvider = append(sum.ByProvider, *p)
	}
	sort.SliceStable(sum.ByProvider, func(i, j int) bool {
		if sum.ByProvider[i].Cost != sum.ByProvider[j].Cost {
			return sum.ByProvider[i].Cost > sum.ByProvider[j].Cost
		}
		return sum.ByProvider[i].Provider < sum.ByProvider[j].Provider
	})
	return sum
}

// max returns the larger of two ints (used for message-count floors).
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
