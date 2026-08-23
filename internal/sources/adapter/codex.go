package adapter

import (
	"encoding/json"
	"strings"
	"time"

	"xusage/internal/model"
)

// Codex rollout record shapes (subset consumed by xusage).
type codexRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	CWD   string          `json:"cwd"`
	Model json.RawMessage `json:"model"` // string in practice; tolerate other shapes
	ID    string          `json:"id"`
}

type codexTokenCountPayload struct {
	Type string `json:"type"`
	Info *struct {
		TotalTokenUsage *codexUsage `json:"total_token_usage"`
		LastTokenUsage  *codexUsage `json:"last_token_usage"`
		Model           *string     `json:"model"`
	} `json:"info"`
}

type codexUsage struct {
	InputTokens           uint64 `json:"input_tokens"`
	CachedInputTokens     uint64 `json:"cached_input_tokens"`
	OutputTokens          uint64 `json:"output_tokens"`
	ReasoningOutputTokens uint64 `json:"reasoning_output_tokens"`
	TotalTokens           uint64 `json:"total_tokens"`
}

// ParseCodexSession parses one Codex rollout file (JSONL). session_meta
// supplies cwd→project and the model; consecutive event_msg token_count
// events collapse into one entry carrying the latest totals, per upstream
// replay/aggregate semantics. When only a grand total is known it lands in
// ExtraTotalTokens; cached input becomes CacheRead; reasoning folds into
// OutputTokens.
func ParseCodexSession(path string) []model.Entry {
	var lines []string
	if err := ReadLines(path, func(l string) { lines = append(lines, l) }); err != nil {
		return nil
	}
	var entries []model.Entry

	project := ""
	sessionID := BaseName(path)
	currentModel := ""
	var stretchTS time.Time
	var stretch model.Entry
	inStretch := false

	flush := func(ts time.Time) {
		if inStretch && stretch.ExtraTotalTokens > 0 {
			stretch.Timestamp = ts
			stretch.Project = project
			stretch.SessionID = sessionID
			stretch.Model = currentModel
			entries = append(entries, stretch)
		}
		inStretch = false
		stretch = model.Entry{}
	}

	for _, line := range lines {
		var rec codexRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		ts := ParseTimestamp(rec.Timestamp)
		switch rec.Type {
		case "session_meta":
			var meta codexSessionMeta
			if err := json.Unmarshal(rec.Payload, &meta); err == nil {
				project = codexProject(meta.CWD)
				if meta.ID != "" {
					sessionID = meta.ID
				}
				if m := codexString(meta.Model); m != "" {
					currentModel = m
				}
			}
		case "turn_context":
			var tc struct {
				Payload struct {
					Model string `json:"model"`
				} `json:"payload"`
			}
			if err := json.Unmarshal([]byte(line), &tc); err == nil && tc.Payload.Model != "" {
				currentModel = tc.Payload.Model
			}
		case "event_msg":
			var p codexTokenCountPayload
			if err := json.Unmarshal(rec.Payload, &p); err != nil || p.Type != "token_count" || p.Info == nil {
				continue
			}
			tot := p.Info.TotalTokenUsage
			if tot == nil {
				continue
			}
			// A repeated identical total is a status re-broadcast, not new usage.
			if inStretch && stretch.ExtraTotalTokens == tot.TotalTokens {
				if !ts.IsZero() {
					stretchTS = ts
				}
				continue
			}
			flush(ts)
			if !ts.IsZero() {
				stretchTS = ts
			}
			inStretch = true
			stretch.Messages = 1
			stretch.InputTokens += tot.InputTokens
			stretch.CacheReadTokens += min64(tot.CachedInputTokens, tot.InputTokens)
			stretch.OutputTokens += tot.OutputTokens + tot.ReasoningOutputTokens
			stretch.ExtraTotalTokens = tot.TotalTokens
			if p.Info.Model != nil && *p.Info.Model != "" {
				currentModel = *p.Info.Model
			}
		}
	}
	flush(stretchTS)
	return entries
}

func codexString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// codexProject reduces a cwd path to its base name (upstream behavior).
func codexProject(cwd string) string {
	cwd = strings.TrimRight(strings.TrimSpace(cwd), `\/`)
	if cwd == "" {
		return ""
	}
	if i := strings.LastIndexAny(cwd, `/\`); i >= 0 {
		return cwd[i+1:]
	}
	return cwd
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
