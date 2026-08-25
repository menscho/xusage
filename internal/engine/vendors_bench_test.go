package engine

import (
	"testing"
	"time"

	"xusage/internal/model"
)

// Benchmarks the two allocation hot-spots in vendors.go: VendorOf (now
// memoized, so repeated lookups for the same interned model name are
// allocation-free) and the ensureTrends scan (now civil-day-index arithmetic
// instead of a per-entry time.Format + map lookup).

func BenchmarkVendorOf(b *testing.B) {
	models := []string{
		"claude-sonnet-4-20250514", "gpt-4o", "gemini-2.5-pro",
		"deepseek-v3", "kimi-k2", "llama-3.1-405b", "qwen3-coder",
		"openrouter/anthropic/claude-opus-4", "bedrock.claude-sonnet-4",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = VendorOf(models[i%len(models)])
	}
}

func BenchmarkEnsureTrends(b *testing.B) {
	// 5000 entries across 8 models and a 7-day window; half inside the window.
	now := time.Now()
	entries := make([]model.Entry, 0, 5000)
	models := []string{"claude-sonnet", "gpt-4o", "gemini-pro", "deepseek-v3", "kimi-k2", "llama-3", "qwen3", "grok-4"}
	for i := 0; i < 5000; i++ {
		day := i % 10 // 0..9, so days 0..6 land in the 7-day window
		entries = append(entries, model.Entry{
			Model:       models[i%len(models)],
			Timestamp:   now.AddDate(0, 0, -day),
			InputTokens: 100, OutputTokens: 50,
		})
	}
	snap := &Snapshot{Entries: entries}
	rows := []model.Row{}
	for _, m := range models {
		rows = append(rows, model.Row{Key: m})
	}
	loc := time.UTC
	today := now.In(loc)
	days := make([]string, 7)
	for i := range days {
		days[i] = today.AddDate(0, 0, i-6).Format("2006-01-02")
	}
	winStart := dayBucket(today) - 6
	o := &Opts{Mode: model.ModeAuto}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Clear the cache so each iteration actually scans.
		trendCache = trendMemo{}
		snap.ensureTrends(rows, o, loc, days, winStart)
	}
}
