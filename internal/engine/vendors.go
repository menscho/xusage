// Package engine — vendor attribution and model/provider rollups for the
// redesigned dashboard (leaderboard, provider matrix, pulse).
package engine

import (
	"sort"
	"strings"
	"sync"
	"time"

	"xusage/internal/model"
)

// vendorCache memoizes VendorOf results by model name. Model names are
// interned by the engine (parse-time interning in every source), so the map
// holds at most a few dozen entries for a lifetime process — bounded, never a
// leak — and the vendor tables are static, so no eviction is ever needed.
// Culling the per-call strings.ToLower allocation matters because callers
// invoke VendorOf per-entry (ByVendor, vendor-filtered) over the whole corpus.
var (
	vendorCacheMu sync.RWMutex
	vendorCache   = map[string]string{}
)

// VendorOf maps a display model name to its vendor family using the first
// segment after any "host/model" prefix and a case-insensitive prefix match.
// Unknown families report "Other" so the UI always has a bucket. Results are
// memoized; the first call for a model allocates, every later hit is free.
func VendorOf(modelName string) string {
	vendorCacheMu.RLock()
	v, ok := vendorCache[modelName]
	vendorCacheMu.RUnlock()
	if ok {
		return v
	}
	v = vendorOf(modelName)
	vendorCacheMu.Lock()
	vendorCache[modelName] = v
	vendorCacheMu.Unlock()
	return v
}

func vendorOf(modelName string) string {
	name := modelName
	if i := strings.LastIndexAny(name, "/:"); i >= 0 && i+1 < len(name) {
		name = name[i+1:] // "openrouter/deepseek/v3" and "gpt-4:2024" style ids
	}
	lower := strings.ToLower(name)
	for cut := 0; cut <= len(lower); cut++ {
		if cut == len(lower) || lower[cut] == '-' || lower[cut] == '_' || lower[cut] == '.' {
			family := lower[:cut]
			if family == "" {
				break
			}
			if v, ok := vendorFamilies[family]; ok {
				return v
			}
			break
		}
	}
	// Multi-segment families ("gpt-5-turbo" family "gpt" above already caught
	// the common case; this pass catches "claude" style ids whose family IS
	// the first token when no separator follows a known prefix).
	for prefix, vendor := range vendorPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return vendor
		}
	}
	return "Other"
}

var vendorFamilies = map[string]string{
	"claude": "Anthropic", "opus": "Anthropic", "sonnet": "Anthropic", "haiku": "Anthropic",
	"gpt": "OpenAI", "o1": "OpenAI", "o3": "OpenAI", "o4": "OpenAI", "chatgpt": "OpenAI",
	"codex": "OpenAI", "text": "OpenAI", "davinci": "OpenAI",
	"gemini": "Google", "gemma": "Google",
	"deepseek": "DeepSeek", "longcat": "LongCat",
	"qwen": "Qwen", "qwq": "Qwen", "kimi": "Moonshot", "k2": "Moonshot", "moonshot": "Moonshot",
	"glm": "Zhipu", "grok": "xAI", "minimax": "MiniMax", "command": "Cohere",
	"mistral": "Mistral", "mixtral": "Mistral", "llama": "Meta", "nova": "Amazon",
	"ernie": "Baidu", "hunyuan": "Tencent", "doubao": "ByteDance", "step": "StepFun",
	"yi": "01.AI", "ox": "Ox", "stealth": "Ox",
}

// vendorPrefixes catches ids whose family token is unknown but whose leading
// brand is definitive ("kimi-k2c", "grok-4-fast").
var vendorPrefixes = map[string]string{
	"claude": "Anthropic", "gpt": "OpenAI", "o1": "OpenAI", "o3": "OpenAI",
	"gemini": "Google", "deepseek": "DeepSeek", "longcat": "LongCat",
	"qwen": "Qwen", "kimi": "Moonshot", "moonshot": "Moonshot", "glm": "Zhipu",
	"grok": "xAI", "minimax": "MiniMax", "command": "Cohere", "mistral": "Mistral",
	"llama": "Meta", "nova": "Amazon", "ernie": "Baidu", "hunyuan": "Tencent",
	"doubao": "ByteDance", "ox-": "Ox",
}

// VendorRow is one vendor family's rollup for /api/providers.
type VendorRow struct {
	Key                 string                 `json:"key"`
	Label               string                 `json:"label"`
	InputTokens         uint64                 `json:"inputTokens"`
	OutputTokens        uint64                 `json:"outputTokens"`
	CacheCreationTokens uint64                 `json:"cacheCreationTokens"`
	CacheReadTokens     uint64                 `json:"cacheReadTokens"`
	ExtraTotalTokens    uint64                 `json:"extraTotalTokens,omitempty"`
	TotalTokens         uint64                 `json:"totalTokens"`
	Cost                float64                `json:"cost"`
	Messages            int64                  `json:"messages"`
	Sessions            int64                  `json:"sessions"`
	Share               float64                `json:"share"`
	Models              []model.ModelBreakdown `json:"models"`
}

// ByVendor rolls filtered entries up by VendorOf(Model), sorted by cost desc.
// Share is each vendor's fraction of total cost.
//
// It iterates s.Entries inline through match() instead of materializing a
// filtered []*model.Entry slice — the same pattern groupBy/ByModel/BySession/
// ByProject/Summary use to skip the ~8 B/entry allocation that filtered()
// performs. ByVendor was the last row endpoint still going through filtered();
// closing that gap keeps every aggregator in the package on one code path.
func (s *Snapshot) ByVendor(o *Opts) []VendorRow {
	loc := o.loc()
	since := parseDay(o.Since, loc)
	until := parseDayEndExclusive(o.Until, loc)
	interned := s.sessionsInterned()
	type acc struct {
		row       VendorRow
		sessions  map[int]bool // interned SessionIdx (or SessionID hashes below)
		sessStr   map[string]bool
		firstSeen map[string]int // model -> index into Models
	}
	groups := map[string]*acc{}
	var order []string
	var totalCost float64
	for i := range s.Entries {
		e := &s.Entries[i]
		if !match(o, loc, since, until, e) {
			continue
		}
		v := VendorOf(e.Model)
		g := groups[v]
		if g == nil {
			g = &acc{firstSeen: map[string]int{}}
			g.row.Key = v
			g.row.Label = v
			groups[v] = g
			order = append(order, v)
		}
		g.row.InputTokens += e.InputTokens
		g.row.OutputTokens += e.OutputTokens
		g.row.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
		g.row.CacheReadTokens += e.CacheReadTokens
		g.row.ExtraTotalTokens += e.ExtraTotalTokens
		cost := e.Cost(o.Mode)
		g.row.Cost += cost
		g.row.Messages += int64(max(e.Messages, 1))
		totalCost += cost
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
		idx, seen := g.firstSeen[e.Model]
		if !seen {
			idx = len(g.row.Models)
			g.firstSeen[e.Model] = idx
			g.row.Models = append(g.row.Models, model.ModelBreakdown{Model: e.Model})
		}
		mb := &g.row.Models[idx]
		mb.InputTokens += e.InputTokens
		mb.OutputTokens += e.OutputTokens
		mb.CacheCreationTokens += e.CacheCreationTokens + e.CacheCreation1h
		mb.CacheReadTokens += e.CacheReadTokens
		mb.ExtraTotalTokens += e.ExtraTotalTokens
		mb.Cost += cost
		if e.MissingPricing {
			mb.MarkMissingPricing()
		}
	}
	rows := make([]VendorRow, 0, len(groups))
	for _, v := range order {
		g := groups[v]
		g.row.TotalTokens = g.row.InputTokens + g.row.OutputTokens +
			g.row.CacheCreationTokens + g.row.CacheReadTokens + g.row.ExtraTotalTokens
		sessions := len(g.sessions)
		if sessions == 0 && len(g.sessStr) > 0 {
			sessions = len(g.sessStr)
		}
		g.row.Sessions = int64(sessions)
		if totalCost > 0 {
			g.row.Share = g.row.Cost / totalCost
		}
		sort.Slice(g.row.Models, func(i, j int) bool { return g.row.Models[i].Cost > g.row.Models[j].Cost })
		rows = append(rows, g.row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cost > rows[j].Cost })
	if o.Last > 0 && len(rows) > o.Last {
		rows = rows[:o.Last]
	}
	return rows
}

// TrendPoint is one day of a model's recent activity for sidebar sparklines
// and leaderboard trends.
type TrendPoint struct {
	Date   string  `json:"date"`
	Tokens uint64  `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// ModelDetail extends ByModel's row shape with vendor attribution and a
// zero-filled 7-day trend for the redesigned leaderboard/models views.
type ModelDetail struct {
	Key                 string                 `json:"key"`
	Vendor              string                 `json:"vendor"`
	InputTokens         uint64                 `json:"inputTokens"`
	OutputTokens        uint64                 `json:"outputTokens"`
	CacheCreationTokens uint64                 `json:"cacheCreationTokens"`
	CacheReadTokens     uint64                 `json:"cacheReadTokens"`
	ExtraTotalTokens    uint64                 `json:"extraTotalTokens,omitempty"`
	TotalTokens         uint64                 `json:"totalTokens"`
	Cost                float64                `json:"cost"`
	Messages            int64                  `json:"messages"`
	Sessions            int64                  `json:"sessions"`
	Share               float64                `json:"share"`
	FirstActivity       time.Time              `json:"firstActivity,omitempty"`
	LastActivity        time.Time              `json:"lastActivity,omitempty"`
	MissingPricing      bool                   `json:"missingPricing,omitempty"`
	Trend7              []TrendPoint           `json:"trend7,omitempty"`
	Models              []model.ModelBreakdown `json:"models,omitempty"` // nil for model rows
}

// trendKey identifies one cached 7-day trend: the snapshot it was computed
// against plus the model name. The snapshot pointer is part of the key so a
// stale trend computed for snapshot S is never served after the poller swaps
// in S'. That also bounds the cache: a snapshot change bumps gen, which
// invalidates every entry at once instead of leaking trends for models the
// user collapsed and never reopened.
type trendKey struct {
	snap  *Snapshot
	model string
}

// trendMemo holds computed Trend7 slices for the current snapshot. get/set
// take the snapshot pointer as a generation guard: the first get against a
// new snapshot clears the whole map, so memory is bounded to the models of
// whichever snapshot is live (at most a handful of trend slices, each 7 points)
// plus the entries queued by concurrent readers during the swap window.
type trendMemo struct {
	mu   sync.Mutex
	snap *Snapshot
	loc  *time.Location
	m    map[trendKey][]TrendPoint
	days []string
	win  int64
}

var trendCache trendMemo

func (c *trendMemo) get(snap *Snapshot, model string) ([]TrendPoint, bool) {
	c.mu.Lock()
	if c.snap != snap {
		c.snap = snap
		c.m = map[trendKey][]TrendPoint{}
	}
	v, ok := c.m[trendKey{snap, model}]
	c.mu.Unlock()
	return v, ok
}

func (c *trendMemo) put(snap *Snapshot, model string, v []TrendPoint) {
	c.mu.Lock()
	if c.snap == snap {
		c.m[trendKey{snap, model}] = v
	}
	c.mu.Unlock()
}

// daysFor returns the 7-day label slice (today-6..today) and the window-start
// bucket for (snap, loc), computing and caching them on first use. The slice
// and winStart are reused across calls for the same (snapshot, timezone), so
// the per-request make+Format allocation is paid at most once per (snap, loc)
// instead of on every /api/models request. It also establishes the
// (snapshot, timezone) generation for the whole trendMemo: a loc change clears
// the trend map so trends computed against a different timezone's winStart are
// never served. Callers must invoke daysFor before get/put in the trend path.
func (c *trendMemo) daysFor(snap *Snapshot, loc *time.Location) ([]string, int64) {
	c.mu.Lock()
	if c.snap != snap || c.loc != loc {
		c.snap = snap
		c.loc = loc
		c.m = map[trendKey][]TrendPoint{}
		c.days = nil
		c.win = 0
	}
	if c.days == nil {
		today := time.Now().In(loc)
		days := make([]string, 7)
		for i := 0; i < 7; i++ {
			days[i] = today.AddDate(0, 0, i-6).Format("2006-01-02")
		}
		c.days = days
		c.win = dayBucket(today) - 6
	}
	days := c.days
	win := c.win
	c.mu.Unlock()
	return days, win
}

// ModelsDetailed is the redesigned /api/models payload. With trend=true each
// row carries a zero-filled Trend7 ending today (snapshot tz). Vendor filter
// applies via Opts.Vendor; share is cost fraction across returned rows.
//
// Trend computation is single-pass: one scan of s.Entries fills every model's
// 7-day buckets, then each row reads its slice out of trendCache. The scan is
// skipped entirely when all requested trends are already cached for this
// snapshot. Per-row allocations are avoided: the days/dayIndex window is built
// once, the zero-filled TrendPoint template is built once and reused for rows
// with no activity in the window, and details is pre-sized to len(rows).
func (s *Snapshot) ModelsDetailed(o *Opts, trend bool) []ModelDetail {
	loc := o.loc()

	rows := s.ByModel(o)
	details := make([]ModelDetail, 0, len(rows))
	var totalCost float64
	for _, r := range rows {
		totalCost += r.Cost
	}

	// Ensure every requested model has a cached trend before assembling rows.
	// days/winStart are cached per (snapshot, timezone) by trendCache.daysFor,
	// so the per-request make+Format allocation is paid once per (snap, loc)
	// instead of on every /api/models call. daysFor also establishes the
	// (snapshot, timezone) generation for the whole trendMemo (clearing trends
	// computed against a different timezone's window), so it must run before
	// ensureTrends/get below. ensureTrends caches a (possibly zero-filled)
	// Trend7 for each model in rows, so the loop reads its slice by reference
	// with no per-row allocation; a cache hit also skips the entry scan.
	var days []string
	var winStart int64
	if trend {
		days, winStart = trendCache.daysFor(s, loc)
		s.ensureTrends(rows, o, loc, days, winStart)
	}

	for _, r := range rows {
		d := ModelDetail{
			Key:                 r.Key,
			Vendor:              VendorOf(r.Key),
			InputTokens:         r.InputTokens,
			OutputTokens:        r.OutputTokens,
			CacheCreationTokens: r.CacheCreationTokens,
			CacheReadTokens:     r.CacheReadTokens,
			ExtraTotalTokens:    r.ExtraTotalTokens,
			TotalTokens:         r.TotalTokens(),
			Cost:                r.Cost,
			Messages:            r.Messages,
			Sessions:            r.Sessions,
			FirstActivity:       r.FirstActivity,
			LastActivity:        r.LastActivity,
			// ByModel already marks the per-model breakdown's MissingPricing
			// flag from entry-level pricing state (aggregate.go); reuse it
			// instead of a redundant per-row pricing.Get().Known lookup.
			MissingPricing: len(r.Models) > 0 && r.Models[0].MissingPricing,
		}
		if totalCost > 0 {
			d.Share = r.Cost / totalCost
		}
		if trend {
			d.Trend7, _ = trendCache.get(s, r.Key)
		}
		details = append(details, d)
	}
	return details
}

// trendAccum is the mutable per-model scratch for a single trend scan. Using a
// fixed [7] backing array keeps the accumulation off the per-entry heap.
type trendAccum struct {
	tokens [7]uint64
	cost   [7]float64
}

// ensureTrends guarantees a cached Trend7 for every model in rows. It runs one
// pass over s.Entries, accumulating into per-model [7] arrays keyed by the
// model names actually requested, then writes a Trend7 slice to trendCache for
// each. Models already cached for this snapshot are left untouched, so
// repeated calls for overlapping model sets only scan the new ones. Snapshot
// identity is the invalidation signal: trendCache is cleared on swap.
func (s *Snapshot) ensureTrends(rows []model.Row, o *Opts, loc *time.Location, days []string, winStart int64) {
	// Collect models not yet cached for this snapshot so we don't recompute
	// them. Lazily allocated (nil until first miss): a fully-cached call (the
	// common case once trends are warm) allocates nothing here, so the
	// poll-time /api/models path zeroes its scratch on the hot hit branch.
	var needed []model.Row
	for _, r := range rows {
		if _, ok := trendCache.get(s, r.Key); !ok {
			needed = append(needed, r)
		}
	}
	if len(needed) == 0 {
		return
	}

	// Restrict accumulation to the models we actually need: a model present in
	// the snapshot but not in the current filtered view is skipped, which both
	// saves work and keeps the per-scan map small.
	needSet := make(map[string]struct{}, len(needed))
	for _, r := range needed {
		needSet[r.Key] = struct{}{}
	}

	acc := make(map[string]*trendAccum, len(needed))
	for i := range s.Entries {
		e := &s.Entries[i]
		if _, ok := needSet[e.Model]; !ok {
			continue
		}
		a := acc[e.Model]
		if a == nil {
			a = &trendAccum{}
			acc[e.Model] = a
		}
		// dayBucket is allocation-free civil-day-index arithmetic; the offset
		// from winStart is the 0..6 bucket index. Entries outside the 7-day
		// window (or with a zero-valued Timestamp) fall through.
		if idx := dayBucket(e.Timestamp.In(loc)) - winStart; idx >= 0 && idx < 7 {
			a.tokens[idx] += e.TotalTokens()
			a.cost[idx] += e.Cost(o.Mode)
		}
	}

	for _, r := range needed {
		a, ok := acc[r.Key]
		if !ok {
			// Model has zero entries in the window: emit a zero-filled slice so
			// the row always carries a well-formed Trend7 when trend=true.
			tp := make([]TrendPoint, 7)
			for i := range days {
				tp[i] = TrendPoint{Date: days[i]}
			}
			trendCache.put(s, r.Key, tp)
			continue
		}
		tp := make([]TrendPoint, 7)
		for i := range days {
			tp[i] = TrendPoint{Date: days[i], Tokens: a.tokens[i], Cost: a.cost[i]}
		}
		trendCache.put(s, r.Key, tp)
	}
}
