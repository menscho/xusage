// Package droid — parsing of Droid settings files into usage entries.
//
// Each *.settings.json file is one session's cumulative token snapshot
// (upstream adapters/droid/src/parser.rs). The file carries a tokenUsage object
// with camelCase counters, a model name (possibly decorated like
// "Claude-Sonnet-4-[Anthropic]" or "custom:..."), and a provider lock with its
// timestamp. A missing model falls back to the sidecar <stem>.jsonl transcript.
package droid

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"xusage/internal/model"
)

// settingsFile mirrors the subset of a Droid settings file we consume.
type settingsFile struct {
	Model                 string          `json:"model"`
	ProviderLock          string          `json:"providerLock"`
	ProviderLockTimestamp string          `json:"providerLockTimestamp"`
	TokenUsage            json.RawMessage `json:"tokenUsage"`
}

// tokenUsage mirrors tokenUsage counters; every field is optional and leniently
// parsed (numbers may arrive as strings).
type tokenUsage struct {
	InputTokens         uint64 `json:"inputTokens"`
	OutputTokens        uint64 `json:"outputTokens"`
	CacheCreationTokens uint64 `json:"cacheCreationTokens"`
	CacheReadTokens     uint64 `json:"cacheReadTokens"`
	ThinkingTokens      uint64 `json:"thinkingTokens"`
	TotalTokens         uint64 `json:"totalTokens"`
}

// parseTokenUsage extracts counters, applying upstream apply_total_token_fallback:
// a reported total beyond the named buckets first backfills zero output, then
// tops up the extra (thinking) bucket. The raw total is consumed afterwards —
// Entry totals sum every bucket, so keeping it too would double count.
func parseTokenUsage(raw json.RawMessage) (tokenUsage, bool) {
	var u tokenUsage
	if len(raw) == 0 || string(raw) == "null" {
		return u, false
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return u, false
	}
	known := u.InputTokens + u.OutputTokens + u.CacheCreationTokens +
		u.CacheReadTokens + u.ThinkingTokens
	if u.TotalTokens > known {
		missing := u.TotalTokens - known
		if u.OutputTokens == 0 {
			u.OutputTokens = missing
		} else {
			u.ThinkingTokens += missing
		}
	}
	u.TotalTokens = 0
	sum := u.InputTokens + u.OutputTokens + u.CacheCreationTokens +
		u.CacheReadTokens + u.ThinkingTokens
	if sum == 0 {
		return u, false
	}
	return u, true
}

// loadSettingsFile parses one *.settings.json into at most one entry.
// Unreadable or unusable files yield (nil, nil) — never an error.
func loadSettingsFile(path string) ([]model.Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, nil // unreadable settings files are skipped, not fatal
	}
	var sf settingsFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, nil // malformed JSON: skip silently like malformed JSONL lines
	}
	usage, ok := parseTokenUsage(sf.TokenUsage)
	if !ok {
		return nil, nil
	}

	provider := normalizeProvider(sf.ProviderLock)
	name := normalizeModelName(sf.Model)
	// Upstream resolution order: settings model → sidecar transcript →
	// provider default. The sidecar is consulted only when the settings file
	// carries no model at all; a model that normalizes to "" falls back to the
	// provider default instead of re-reading the sidecar.
	if sf.Model == "" {
		if sidecar := modelFromSidecarJSONL(path); sidecar != "" {
			name = sidecar
		}
	}
	if name == "" {
		name = defaultModelFor(provider)
	} else if p := inferProviderFromModel(name); provider == "unknown" && p != "" {
		provider = p
	}
	if name == "" {
		return nil, nil
	}

	ts := settingsTimestamp(sf.ProviderLockTimestamp, path)
	sessionID := strings.TrimSuffix(baseName(path), ".settings.json")
	if sessionID == "" || sessionID == path {
		sessionID = "unknown"
	}

	e := model.Entry{
		ID:                  "droid:" + sessionID,
		Timestamp:           ts,
		Model:               name,
		RawModel:            firstNonEmpty(sf.Model, name),
		Project:             projectFor(),
		SessionID:           sessionID,
		Version:             "",
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		Messages:            1,
	}
	// Reasoning tokens ride as the extra bucket so totals include them exactly
	// once (mirrors upstream extra_total_tokens = thinking_tokens).
	e.ExtraTotalTokens = usage.ThinkingTokens
	return []model.Entry{e}, nil
}

// projectFor names the project column for droid entries; upstream hardcodes
// the agent name because settings files carry no working directory.
func projectFor() string { return "droid" }

// settingsTimestamp prefers providerLockTimestamp and falls back to the file's
// modification time.
func settingsTimestamp(lockTS, path string) time.Time {
	if lockTS != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
			if t, err := time.Parse(layout, lockTS); err == nil {
				return t.UTC()
			}
		}
	}
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UTC()
	}
	return time.Time{}
}

// normalizeModelName ports upstream normalize_droid_model_name: strip a
// "custom:" prefix, drop [bracket] decorations, lowercase, and collapse
// dots/whitespace/dashes into single dashes.
func normalizeModelName(m string) string {
	m = strings.TrimPrefix(strings.TrimSpace(m), "custom:")
	var b strings.Builder
	depth := 0
	for _, r := range m {
		switch r {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	s := strings.ToLower(strings.Trim(b.String(), "-"))
	var out strings.Builder
	dash := false
	for _, r := range s {
		next := r
		if r == '.' || r == ' ' || r == '\t' || r == '-' {
			next = '-'
		}
		if next == '-' {
			if dash {
				continue
			}
			dash = true
		} else {
			dash = false
		}
		out.WriteRune(next)
	}
	return strings.Trim(out.String(), "-")
}

// normalizeProvider maps raw provider-lock spellings onto canonical names,
// mirroring upstream normalize_droid_provider.
func normalizeProvider(v string) string {
	v = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(v, "-", "_")))
	switch v {
	case "":
		return "unknown"
	case "claude", "anthropic":
		return "anthropic"
	case "openai":
		return "openai"
	case "google", "google_ai", "gemini", "vertex", "vertex_ai":
		return "google"
	case "xai", "x_ai", "grok":
		return "xai"
	default:
		return v
	}
}

// inferProviderFromModel guesses the provider from well-known model names for
// sessions without an explicit provider lock.
func inferProviderFromModel(model string) string {
	switch {
	case containsAny(model, "claude", "opus", "sonnet", "haiku"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-"), containsAny(model, "-gpt-", "chatgpt"):
		return "openai"
	case containsAny(model, "gemini"):
		return "google"
	case containsAny(model, "grok"):
		return "xai"
	default:
		return ""
	}
}

// defaultModelFor is the placeholder model when none could be resolved.
// "unknown" keeps the entry alive with zero pricing impact (unknown models
// cost 0), mirroring upstream default_model_from_provider.
func defaultModelFor(provider string) string {
	switch provider {
	case "anthropic":
		return "claude-unknown"
	case "openai":
		return "gpt-unknown"
	case "google":
		return "gemini-unknown"
	case "xai":
		return "grok-unknown"
	default:
		return "unknown"
	}
}

// modelFromSidecarJSONL scans the sibling <stem>.jsonl transcript for a line
// carrying "Model: <name>" (first 500 lines), as upstream does.
func modelFromSidecarJSONL(settingsPath string) string {
	stem := strings.TrimSuffix(baseName(settingsPath), ".settings.json")
	if stem == "" {
		return ""
	}
	// Join next to the settings file: strip its base name, keep the directory
	// part including the trailing separator.
	dir := settingsPath[:len(settingsPath)-len(baseName(settingsPath))]
	sidecar := dir + stem + ".jsonl"
	data, err := os.ReadFile(sidecar)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 500 {
		lines = lines[:500]
	}
	for _, line := range lines {
		i := strings.Index(line, "Model:")
		if i < 0 {
			continue
		}
		tail := line[i+len("Model:"):]
		end := strings.IndexAny(tail, `"\[`)
		if end >= 0 {
			tail = tail[:end]
		}
		tail = strings.TrimSpace(tail)
		if tail == "" {
			continue
		}
		if n := normalizeModelName(tail); n != "" {
			return n
		}
	}
	return ""
}

// small string helpers kept local to avoid importing a shared util package.
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	if i := strings.LastIndexByte(p, '\\'); i >= 0 {
		p = p[i+1:]
	}
	return p
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
