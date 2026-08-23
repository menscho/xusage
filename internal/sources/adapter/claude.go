package adapter

import (
	"encoding/json"
	"strings"

	"xusage/internal/model"
)

// ClaudeLine is the subset of a Claude Code JSONL record xusage consumes.
// Pointer fields distinguish "absent" from "explicitly null": per SPEC §3.1 a
// null id/cwd/model/speed/costUSD/version/sessionId/requestId/isApiErrorMessage/
// cache_read_input_tokens/cache_creation_input_tokens rejects the line.
type ClaudeLine struct {
	Type        string   `json:"type"`
	Timestamp   string   `json:"timestamp"`
	SessionID   *string  `json:"sessionId"`
	RequestID   *string  `json:"requestId"`
	Version     *string  `json:"version"`
	CostUSD     *float64 `json:"costUSD"`
	CWD         *string  `json:"cwd"`
	IsSidechain *bool    `json:"isSidechain"`
	IsAPIError  *bool    `json:"isApiErrorMessage"`
	GitBranch   string   `json:"gitBranch"`
	Message     struct {
		ID    *string     `json:"id"`
		Model *string     `json:"model"`
		Role  string      `json:"role"`
		Usage claudeUsage `json:"usage"`
	} `json:"message"`
}

type claudeUsage struct {
	InputTokens   uint64          `json:"input_tokens"`
	OutputTokens  uint64          `json:"output_tokens"`
	CacheCreation json.RawMessage `json:"cache_creation"` // number or {ephemeral_5m_input_tokens,...}
	CacheRead     *uint64         `json:"cache_read_input_tokens"`
	Speed         *string         `json:"speed"`
	ServiceTier   string          `json:"service_tier"`
}

// ParseClaudeLine applies the shared Claude-shape rules (SPEC §3.1) to one
// JSONL line. ok is false when the line carries no usable usage entry.
func ParseClaudeLine(line string) (model.Entry, bool) {
	if !strings.Contains(line, `"usage":{`) {
		return model.Entry{}, false
	}
	var cl ClaudeLine
	if err := json.Unmarshal([]byte(line), &cl); err != nil {
		return model.Entry{}, false
	}
	u := cl.Message.Usage

	// Null-field rejection: any of these explicitly null disqualifies the line.
	if hasNullField(line, "id") || hasNullField(line, "cwd") || hasNullField(line, "model") ||
		hasNullField(line, "speed") || hasNullField(line, "costUSD") || hasNullField(line, "version") ||
		hasNullField(line, "sessionId") || hasNullField(line, "requestId") ||
		hasNullField(line, "isApiErrorMessage") ||
		hasNullField(line, "cache_read_input_tokens") ||
		hasNullField(line, "cache_creation_input_tokens") {
		return model.Entry{}, false
	}

	// Presence + validity checks for fields that may be present-but-empty.
	version := ""
	if cl.Version != nil {
		if !isSemverPrefix(*cl.Version) {
			return model.Entry{}, false
		}
		version = *cl.Version
	}
	sessionID := ""
	if cl.SessionID != nil {
		if *cl.SessionID == "" {
			return model.Entry{}, false
		}
		sessionID = *cl.SessionID
	}
	requestID := ""
	if cl.RequestID != nil {
		if *cl.RequestID == "" {
			return model.Entry{}, false
		}
		requestID = *cl.RequestID
	}
	rawModel := ""
	if cl.Message.Model != nil {
		if *cl.Message.Model == "" {
			return model.Entry{}, false
		}
		rawModel = *cl.Message.Model
	}
	msgID := ""
	if cl.Message.ID != nil {
		if *cl.Message.ID == "" {
			return model.Entry{}, false
		}
		msgID = *cl.Message.ID
	}
	if rawModel == "<synthetic>" {
		return model.Entry{}, false
	}

	ts := ParseTimestamp(cl.Timestamp)
	if ts.IsZero() {
		return model.Entry{}, false
	}

	e := model.Entry{
		ID:           msgID,
		RequestID:    requestID,
		Timestamp:    ts,
		RawModel:     rawModel,
		Model:        rawModel,
		SessionID:    sessionID,
		Version:      version,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		Messages:     1,
		IsAPIError:   cl.IsAPIError != nil && *cl.IsAPIError,
		IsSidechain:  cl.IsSidechain != nil && *cl.IsSidechain,
		ReportedCost: cl.CostUSD,
	}
	if u.CacheRead != nil {
		e.CacheReadTokens = *u.CacheRead
	}

	// cache_creation: flat number (5m bucket) or object with 5m/1h split.
	cc5, cc1h := parseCacheCreation(u.CacheCreation)
	e.CacheCreationTokens = cc5
	e.CacheCreation1h = cc1h

	// speed fast → display-model suffix; standard is ignored.
	if u.Speed != nil && *u.Speed == "fast" {
		e.SpeedFast = true
		e.Model += "-fast"
	}
	return e, true
}

// parseCacheCreation decodes the two observed shapes of cache_creation:
// a bare token count (undifferentiated → 5m bucket) or an object carrying
// ephemeral_5m_input_tokens / ephemeral_1h_input_tokens.
func parseCacheCreation(raw json.RawMessage) (cc5, cc1h uint64) {
	if len(raw) == 0 {
		return 0, 0
	}
	var flat uint64
	if err := json.Unmarshal(raw, &flat); err == nil {
		return flat, 0
	}
	var obj struct {
		Ephemeral5m *uint64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h *uint64 `json:"ephemeral_1h_input_tokens"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, 0
	}
	if obj.Ephemeral5m != nil {
		cc5 = *obj.Ephemeral5m
	}
	if obj.Ephemeral1h != nil {
		cc1h = *obj.Ephemeral1h
	}
	return cc5, cc1h
}

// hasNullField reports whether the line contains `"key":null` outside of
// strings. The scan is textual on purpose: it must run before full decoding
// and cannot confuse `"key":"null"` with a real null.
func hasNullField(line, key string) bool {
	needle := `"` + key + `":`
	idx := strings.Index(line, needle)
	if idx < 0 {
		return false
	}
	rest := strings.TrimLeft(line[idx+len(needle):], " \t")
	return strings.HasPrefix(rest, "null")
}

// isSemverPrefix accepts versions like "1", "1.2", "1.2.3" and tolerates a
// trailing suffix ("1.2.3-beta.4"); anything else fails the semver check.
func isSemverPrefix(v string) bool {
	parts := strings.SplitN(v, "-", 2)
	digits := strings.Split(parts[0], ".")
	if len(digits) < 1 || len(digits) > 3 {
		return false
	}
	for _, d := range digits {
		if d == "" {
			return false
		}
		for _, c := range d {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
