// Package engine — vendor attribution and model/provider rollups for the
// redesigned dashboard (leaderboard, provider matrix, pulse).
package engine

import (
	"sort"
	"strings"
	"time"

	"xusage/internal/model"
	"xusage/internal/pricing"
)

// VendorOf maps a display model name to its vendor family using the first
// segment after any "host/model" prefix and a case-insensitive prefix match.
// Unknown families report "Other" so the UI always has a bucket.
func VendorOf(modelName string) string {
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
func (s *Snapshot) ByVendor(o *Opts) []VendorRow {
	entries := s.filtered(o) // []*model.Entry
	type acc struct {
		row       VendorRow
		sessions  map[string]bool
		firstSeen map[string]int // model -> index into Models
	}
	groups := map[string]*acc{}
	var order []string
	var totalCost float64
	for _, e := range entries {
		v := VendorOf(e.Model)
		g := groups[v]
		if g == nil {
			g = &acc{sessions: map[string]bool{}, firstSeen: map[string]int{}}
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
		g.row.Cost += e.Cost(o.Mode)
		g.row.Messages += int64(max(e.Messages, 1))
		totalCost += e.Cost(o.Mode)
		if e.SessionID != "" {
			g.sessions[e.SessionID] = true
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
		mb.Cost += e.Cost(o.Mode)
		if e.MissingPricing {
			mb.MarkMissingPricing()
		}
	}
	rows := make([]VendorRow, 0, len(groups))
	for _, v := range order {
		g := groups[v]
		g.row.TotalTokens = g.row.InputTokens + g.row.OutputTokens +
			g.row.CacheCreationTokens + g.row.CacheReadTokens + g.row.ExtraTotalTokens
		g.row.Sessions = int64(len(g.sessions))
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

// ModelsDetailed is the redesigned /api/models payload. With trend=true each
// row carries a zero-filled Trend7 ending today (snapshot tz). Vendor filter
// applies via Opts.Vendor; share is cost fraction across returned rows.
func (s *Snapshot) ModelsDetailed(o *Opts, trend bool) []ModelDetail {
	loc := o.loc()
	today := time.Now().In(loc)
	days := make([]string, 7)
	dayIndex := map[string]int{}
	for i := 0; i < 7; i++ {
		d := today.AddDate(0, 0, i-6)
		days[i] = d.Format("2006-01-02")
		dayIndex[days[i]] = i
	}

	rows := s.ByModel(o)
	details := make([]ModelDetail, 0, len(rows))
	var totalCost float64
	for _, r := range rows {
		totalCost += r.Cost
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
			MissingPricing:      !pricing.Get().Known(r.Key) && r.TotalTokens() > 0,
		}
		if totalCost > 0 {
			d.Share = r.Cost / totalCost
		}
		if trend {
			d.Trend7 = make([]TrendPoint, 7)
			for i, day := range days {
				d.Trend7[i] = TrendPoint{Date: day}
			}
			for i := range s.Entries {
				e := &s.Entries[i]
				if e.Model != r.Key {
					continue
				}
				if idx, ok := dayIndex[e.Timestamp.In(loc).Format("2006-01-02")]; ok {
					d.Trend7[idx].Tokens += e.TotalTokens()
					d.Trend7[idx].Cost += e.Cost(o.Mode)
				}
			}
		}
		details = append(details, d)
	}
	return details
}
