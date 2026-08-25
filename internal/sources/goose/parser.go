package goose

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
	"xusage/internal/sources/adapter"
)

// createdAtJSON reads created_at loosely: goose exports may carry epoch
// millis (number) or text timestamps.
type createdAtJSON string

// UnmarshalJSON accepts numbers and strings alike.
func (c *createdAtJSON) UnmarshalJSON(data []byte) error {
	var n int64
	if json.Unmarshal(data, &n) == nil {
		*c = createdAtJSON(strconv.FormatInt(n, 10))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*c = createdAtJSON(s)
	return nil
}

// String exposes the raw text form for parseTimestamp.
func (c createdAtJSON) String() string { return string(c) }

// sessionJSON mirrors one row of goose's sessions table as exported to JSON.
// Field names match the SQLite columns upstream queries.
type sessionJSON struct {
	ID              string          `json:"id"`
	ModelConfig     json.RawMessage `json:"model_config_json"`
	Provider        string          `json:"provider_name"`
	CreatedAt       createdAtJSON   `json:"created_at"`
	TotalTokens     int64           `json:"total_tokens"`
	InputTokens     int64           `json:"input_tokens"`
	OutputTokens    int64           `json:"output_tokens"`
	AccTotalTokens  int64           `json:"accumulated_total_tokens"`
	AccInputTokens  int64           `json:"accumulated_input_tokens"`
	AccOutputTokens int64           `json:"accumulated_output_tokens"`
}

// Parse extracts usage entries from a goose sessions export (JSONL, one object
// per line, or a single JSON object/array). Rows without a usable id, model
// config, timestamp, or token count are skipped. Unreadable files are an
// error; malformed rows never are.
func Parse(path string) ([]model.Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []sessionJSON
	switch {
	case strings.HasSuffix(strings.ToLower(path), ".jsonl"):
		rows = decodeJSONL(data)
	default:
		obj, err := decodeJSONValue(data)
		if err != nil {
			return nil, nil // not a goose export: nothing to report
		}
		rows = obj
	}

	entries := make([]model.Entry, 0, len(rows))
	for i := range rows {
		if e, ok := rowToEntry(&rows[i]); ok {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// decodeJSONL parses newline-delimited objects, skipping malformed lines. It
// scans the raw bytes directly (no string copy + Split) and trims each line in
// place, mirroring claude/codex streaming parsers.
func decodeJSONL(data []byte) []sessionJSON {
	var rows []sessionJSON
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || !bytes.Contains(line, []byte(`"`)) {
			continue
		}
		var row sessionJSON
		if json.Unmarshal(line, &row) == nil && row.ID != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

// decodeJSONValue accepts a single object or an array of objects.
func decodeJSONValue(data []byte) ([]sessionJSON, error) {
	var arr []sessionJSON
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}
	var one sessionJSON
	if err := json.Unmarshal(data, &one); err == nil && one.ID != "" {
		return []sessionJSON{one}, nil
	}
	return nil, io.ErrUnexpectedEOF
}

// rowToEntry ports upstream row_to_entry: accumulated_* columns win over the
// plain totals when set, reasoning tokens fold into output for pricing while
// surfacing the remainder as ExtraTotalTokens, and empty sessions drop out.
func rowToEntry(row *sessionJSON) (model.Entry, bool) {
	ts, ok := parseTimestamp(row.CreatedAt.String())
	if !ok || strings.TrimSpace(row.ID) == "" {
		return model.Entry{}, false
	}
	modelName := adapter.Intern(parseModelConfig(row.ModelConfig))
	if modelName == "" {
		return model.Entry{}, false // query filters NULL/empty model configs
	}

	in := pickToken(row.AccInputTokens, row.InputTokens)
	out := pickToken(row.AccOutputTokens, row.OutputTokens)
	total := pickToken(row.AccTotalTokens, row.TotalTokens)
	if total == 0 {
		total = in + out
	}
	if in == 0 && out == 0 && total == 0 {
		return model.Entry{}, false
	}
	// Goose's grand total includes reasoning tokens upstream; keep the split
	// in Input/Output and surface only the remainder separately so aggregation
	// neither drops nor double-counts it.
	extra := uint64(0)
	if total > in+out {
		extra = total - in - out
	}

	// No path layout exists for a database store, so mirror upstream
	// row_to_entry and tag every entry with the fixed "goose" project.
	e := model.Entry{
		ID:               row.ID,
		RequestID:        row.ID,
		Timestamp:        ts.UTC(),
		Model:            modelName,
		RawModel:         modelName,
		Project:          "goose",
		SessionID:        row.ID,
		InputTokens:      in,
		OutputTokens:     out,
		ExtraTotalTokens: extra,
		Messages:         1,
	}
	return e, true
}

// pickToken mirrors upstream read_token_value: non-positive values mean "not
// recorded" and fall through to the fallback column.
func pickToken(primary, fallback int64) uint64 {
	if primary > 0 {
		return uint64(primary)
	}
	if fallback > 0 {
		return uint64(fallback)
	}
	return 0
}

// modelConfigJSON mirrors the model_config_json blob; only model_name is used.
type modelConfigJSON struct {
	ModelName string `json:"model_name"`
}

// parseModelConfig extracts the human-readable model name from the
// model_config_json column value. The column may hold a JSON object blob or a
// JSON-encoded string wrapping one; both unwrap here.
func parseModelConfig(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = []byte(s) // string-wrapped blob: use the decoded contents
	}
	var cfg modelConfigJSON
	if json.Unmarshal(raw, &cfg) == nil {
		return strings.TrimSpace(cfg.ModelName)
	}
	return ""
}

// parseTimestamp ports parse_goose_timestamp: epoch seconds/millis, RFC 3339,
// SQLite-style "YYYY-MM-DD HH:MM:SS", and bare dates all parse; everything
// else fails. All results are UTC.
func parseTimestamp(value string) (time.Time, bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n <= 0 {
			return time.Time{}, false
		}
		if n > 1_000_000_000_000 { // already milliseconds
			return time.UnixMilli(n).UTC(), true
		}
		return time.Unix(n, 0).UTC(), true
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	// SQLite datetime text: "YYYY-MM-DD HH:MM:SS[.fff]" (19+ chars).
	if len(v) >= 19 && v[4] == '-' && v[7] == '-' && (v[10] == ' ' || v[10] == 'T') {
		t, err := time.Parse("2006-01-02 15:04:05", v[:19])
		if err == nil {
			return t.UTC(), true
		}
	}
	// Bare date: "YYYY-MM-DD".
	if len(v) == 10 && v[4] == '-' && v[7] == '-' {
		t, err := time.Parse("2006-01-02", v)
		if err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
