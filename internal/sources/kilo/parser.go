package kilo

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"xusage/internal/model"
)

// message mirrors upstream KiloMessage: only the fields ccusage consumes are
// declared. Lenient decoding keeps records whose unexpected field types would
// otherwise fail the whole line (see upstream adapters/common/src/jsonl.rs).
type message struct {
	Role       *string         `json:"role"`       // assistant messages only
	ID         *string         `json:"id"`         // message id, empty dropped
	ModelID    *string         `json:"modelID"`    // model as logged, empty dropped
	SessionID  *string         `json:"session_id"` // falls back to the row's session
	ProviderID *string         `json:"providerID"` // informational only
	Cost       json.RawMessage `json:"cost"`       // lenient: number or anything else
	Tokens     json.RawMessage `json:"tokens"`     // lenient: object or anything else
	Time       struct {
		Created *int64 `json:"created"` // seconds or milliseconds epoch
	} `json:"time"`
}

// lenientCost mirrors upstream jsonl::lenient_f64 on the cost column: a JSON
// number (or numeric string) yields a cost, any other value yields nil — never
// a reason to fail the record.
func lenientCost(raw json.RawMessage) *float64 {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// tokens mirrors upstream KiloTokens/KiloCache.
type tokens struct {
	Input     uint64          `json:"input"`
	Output    uint64          `json:"output"`
	Cache     json.RawMessage `json:"cache"` // lenient: object or anything else
	Reasoning uint64          `json:"reasoning"`
	Total     uint64          `json:"total"`
}

type cacheTokens struct {
	Read  uint64 `json:"read"`
	Write uint64 `json:"write"`
}

// parseFile decodes a JSONL file of Kilo message records into entries.
// Malformed lines are skipped; only an unreadable file returns an error.
func parseFile(path string) ([]model.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseReader(path, f)
}

func parseReader(path string, r io.Reader) ([]model.Entry, error) {
	var entries []model.Entry
	lineNo := 0
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !bytes.Contains(line, []byte(`"tokens"`)) {
			continue // fast prefilter: every usable record carries tokens
		}
		if e, ok := decodeLine(path, line, lineNo); ok {
			entries = append(entries, e)
		}
		lineNo++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// decodeLine converts one JSON line into an entry, reporting whether the
// record is usable. path and lineNo exist only to build the fallback message
// id. Rejection rules mirror upstream message_value_to_entry.
func decodeLine(path string, line []byte, lineNo int) (model.Entry, bool) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		return model.Entry{}, false
	}
	if m.Role == nil || *m.Role != "assistant" {
		return model.Entry{}, false
	}
	tok, ok := decodeTokens(m.Tokens)
	if !ok {
		return model.Entry{}, false
	}
	if m.ModelID == nil || *m.ModelID == "" {
		return model.Entry{}, false
	}
	if m.Time.Created == nil || *m.Time.Created <= 0 {
		return model.Entry{}, false // no timestamp: not plottable
	}

	// Total-token fallback (upstream apply_total_token_fallback): reasoning
	// tokens are unpriced metadata, so the provider grand total first lands in
	// ExtraTotalTokens via reasoning; a remaining gap backfills zero output,
	// otherwise it spills into ExtraTotalTokens.
	extra := tok.Reasoning
	known := tok.Input + tok.Output + cacheWrite(tok) + cacheRead(tok) + extra
	var missing uint64
	if tok.Total > known {
		missing = tok.Total - known
	}
	if missing > 0 {
		if tok.Output == 0 {
			tok.Output = missing
		} else {
			extra += missing
		}
	}
	if tok.Input == 0 && tok.Output == 0 && cacheWrite(tok) == 0 && cacheRead(tok) == 0 && extra == 0 {
		return model.Entry{}, false // nothing measurable
	}

	ts := normalizeTimestamp(*m.Time.Created)
	session := ""
	if m.SessionID != nil {
		session = *m.SessionID
	}
	id := ""
	if m.ID != nil {
		id = *m.ID
	}
	if id == "" {
		// Upstream falls back to "<db path>:<row id>" so every message keeps a
		// distinct dedup identity; the JSONL subset has no row id, so the file
		// path plus line position stands in for it.
		id = path + ":" + strconv.Itoa(lineNo)
	}

	e := model.Entry{
		ID:                  id,
		Timestamp:           ts,
		Model:               *m.ModelID,
		RawModel:            *m.ModelID,
		SessionID:           session,
		InputTokens:         tok.Input,
		OutputTokens:        tok.Output,
		CacheCreationTokens: cacheWrite(tok),
		CacheReadTokens:     cacheRead(tok),
		ExtraTotalTokens:    extra,
		Messages:            1,
	}
	if m.Cost != nil {
		e.ReportedCost = lenientCost(m.Cost)
	}
	return e, true
}

// decodeTokens leniently extracts the token block: objects decode field-wise,
// any other JSON value (or a decode failure) yields a zero block that stays
// usable, mirroring upstream lenient_object.
func decodeTokens(raw json.RawMessage) (tokens, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return tokens{}, false
	}
	var t tokens
	if err := json.Unmarshal(raw, &t); err != nil {
		return tokens{}, false
	}
	return t, true
}

// cacheRead returns the cache-read count, tolerating a non-object cache field.
func cacheRead(t tokens) uint64 {
	var c cacheTokens
	if err := json.Unmarshal(t.Cache, &c); err != nil {
		return 0
	}
	return c.Read
}

// cacheWrite returns the cache-write count, tolerating a non-object cache field.
func cacheWrite(t tokens) uint64 {
	var c cacheTokens
	if err := json.Unmarshal(t.Cache, &c); err != nil {
		return 0
	}
	return c.Write
}

// normalizeTimestamp converts epoch seconds or milliseconds to UTC time,
// mirroring upstream normalize_timestamp.
func normalizeTimestamp(v int64) time.Time {
	if v < 1_000_000_000_000 {
		v *= 1000
	}
	return time.UnixMilli(v).UTC()
}
