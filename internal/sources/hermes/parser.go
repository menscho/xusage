// Mapping of sessions rows onto model.Entry.
//
// Mirrors upstream rust/adapters/hermes/src/parser.rs: one row per session,
// reasoning tokens surface as ExtraTotalTokens, actual cost wins over
// estimated cost, and empty or all-zero rows are skipped.
package hermes

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"xusage/internal/model"
)

// sessionRow is one row of the Hermes sessions table, addressed by column name.
type sessionRow struct {
	vals  []any
	cols  map[string]int
	rowid int64
}

// newSessionRow indexes a decoded record by column name.
func newSessionRow(cols []string, vals []any, rowid int64) *sessionRow {
	m := make(map[string]int, len(cols))
	for i, c := range cols {
		if _, dup := m[c]; !dup {
			m[c] = i
		}
	}
	return &sessionRow{vals: vals, cols: m, rowid: rowid}
}

// str returns a trimmed string value by column; missing/NULL columns are "".
// Numeric values stringify because SQLite columns are dynamically typed
// (e.g. an INTEGER PRIMARY KEY substituted from the rowid).
func (r *sessionRow) str(col string) string {
	i, ok := r.cols[col]
	if !ok || i >= len(r.vals) {
		return ""
	}
	switch v := r.vals[i].(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	default:
		return ""
	}
}

// num returns a non-negative float by column; 0 when absent or negative.
func (r *sessionRow) num(col string) float64 {
	v, _ := r.numOK(col)
	return v
}

func (r *sessionRow) numOK(col string) (float64, bool) {
	i, ok := r.cols[col]
	if !ok || i >= len(r.vals) || r.vals[i] == nil {
		return 0, false
	}
	var f float64
	switch v := r.vals[i].(type) {
	case int64:
		f = float64(v)
	case float64:
		f = v
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, false
	}
	return f, true
}

// uintCol returns a truncated non-negative integer by column.
func (r *sessionRow) uintCol(col string) uint64 {
	f, ok := r.numOK(col)
	if !ok {
		return 0
	}
	if f > maxUint64AsFloat {
		return math.MaxUint64
	}
	return uint64(math.Trunc(f))
}

// maxUint64AsFloat is the largest uint64 exactly representable as float64.
const maxUint64AsFloat = float64(1 << 53)

// entry converts the row to a model.Entry per the upstream parser rules:
// skip rows without a usable id/model or an invalid started_at, skip all-zero
// usage rows, fold reasoning tokens into ExtraTotalTokens, and prefer actual
// over estimated cost.
func (r *sessionRow) entry() (model.Entry, bool) {
	id := r.str("id")
	if id == "" {
		id = fmt.Sprintf("hermes-row-%d", r.rowid)
	}
	rawModel := r.str("model")
	if rawModel == "" {
		return model.Entry{}, false // upstream filters these out in SQL
	}
	ts, tsOK := r.timestamp()
	if !tsOK {
		return model.Entry{}, false // upstream timestamp_from_number rejects these
	}
	input := r.uintCol("input_tokens")
	output := r.uintCol("output_tokens")
	cacheRead := r.uintCol("cache_read_tokens")
	cacheCreate := r.uintCol("cache_write_tokens")
	reasoning := r.uintCol("reasoning_tokens")
	cost := r.cost()

	if input == 0 && output == 0 && cacheRead == 0 && cacheCreate == 0 &&
		reasoning == 0 && cost == nil {
		return model.Entry{}, false // nothing billable recorded for this session
	}

	var reported *float64
	if cost != nil {
		v := *cost
		reported = &v
	}
	return model.Entry{
		ID:                  id,
		Timestamp:           ts,
		Model:               rawModel,
		RawModel:            rawModel,
		Project:             "hermes",
		SessionID:           id,
		InputTokens:         input,
		OutputTokens:        output,
		CacheCreationTokens: cacheCreate,
		CacheReadTokens:     cacheRead,
		ExtraTotalTokens:    reasoning,
		ReportedCost:        reported,
		Messages:            r.messages(),
	}, true
}

// cost resolves the provider-reported cost. Upstream computes
// actual_cost.or(estimated_cost): a positive actual wins, but an absent or
// zero actual falls through to the estimated value (only negative/invalid
// values are treated as missing). Only positive values are reported.
func (r *sessionRow) cost() *float64 {
	actual, okActual := r.numOK("actual_cost_usd")
	estimated, okEstimated := r.numOK("estimated_cost_usd")
	if okActual && actual > 0 {
		v := actual
		return &v
	}
	if okEstimated && estimated > 0 {
		v := estimated
		return &v
	}
	return nil
}

// messages returns message_count as-is (upstream reads it without clamping;
// the aggregation treats zero as one contribution).
func (r *sessionRow) messages() int {
	return int(clampInt(r.uintCol("message_count")))
}

// clampInt saturates a uint64 at math.MaxInt.
func clampInt(n uint64) uint64 {
	const capInt = uint64(^uint(0) >> 1)
	if n > capInt {
		return capInt
	}
	return n
}

// timestamp decodes started_at. Values above 1e12 are epoch milliseconds;
// smaller values are epoch seconds (mirroring upstream timestamp_from_number).
// The boolean result reports whether the row carries a usable timestamp;
// upstream drops rows that do not.
func (r *sessionRow) timestamp() (time.Time, bool) {
	f, ok := r.numOK("started_at")
	if !ok || f <= 0 {
		return time.Time{}, false
	}
	if f > 1e12 {
		f = math.Trunc(f)
	} else {
		f = math.Trunc(f) * 1000
	}
	ms := f
	if ms > maxUint64AsFloat*1000 {
		ms = maxUint64AsFloat * 1000
	}
	return time.UnixMilli(int64(ms)).UTC(), true
}
