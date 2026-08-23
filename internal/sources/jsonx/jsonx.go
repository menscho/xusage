// Package jsonx holds small JSONL/JSON helpers shared by the source adapters.
//
// Adapters all face the same problems: walk newline-delimited JSON cheaply,
// pull loosely-typed token counters out of arbitrary shapes, and normalize the
// many timestamp spellings providers use. These helpers keep each adapter
// focused on its own field mapping. They are intentionally dependency-free
// (stdlib encoding/json only).
package jsonx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func bufioReader(f *os.File) *bufio.Reader { return bufio.NewReaderSize(f, 1<<20) }

func timeParse(layout, value string) (time.Time, error) { return time.Parse(layout, value) }

// WalkJSONL reads path as newline-delimited JSON and calls fn for every
// non-blank line until fn returns false. Read errors (other than a trailing
// line without a newline) abort the walk and are returned.
func WalkJSONL(path string, fn func(line []byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufioReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 && !fn(trimmed) {
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// Unmarshal decodes one JSON value, ignoring malformed input (adapters skip
// bad lines rather than fail the file).
func Unmarshal(line []byte, v any) error {
	return json.Unmarshal(line, v)
}

// Obj returns a nested object field; nil when absent or not an object.
func Obj(v any, key string) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	o, _ := m[key].(map[string]any)
	return o
}

// Arr returns a nested array field; nil when absent or not an array.
func Arr(v any, key string) []any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	a, _ := m[key].([]any)
	return a
}

// Has reports whether key exists in the object (even with a null value).
func Has(v any, key string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[key]
	return ok
}

// Get returns the raw field value; nil when absent.
func Get(v any, key string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m[key]
}

// Str returns a non-empty string field; "" otherwise.
func Str(v any, key string) string {
	s, _ := Get(v, key).(string)
	return strings.TrimSpace(s)
}

// StrOrNull mirrors Str but distinguishes "absent" from "present".
func StrPtr(v any, key string) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

// U64 returns a numeric field as uint64, accepting floats and numerals.
func U64(v any, key string) uint64 {
	return U64Of(Get(v, key))
}

// U64Of coerces a raw JSON value to uint64 (numbers, float-typed numbers,
// numeric strings). Non-numeric input yields 0.
func U64Of(raw any) uint64 {
	switch n := raw.(type) {
	case float64:
		if n <= 0 {
			return 0
		}
		if n > 9.2e18 {
			return 0
		}
		return uint64(n)
	case string:
		var u uint64
		s := strings.TrimSpace(n)
		if s == "" {
			return 0
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0
			}
			u = u*10 + uint64(c-'0')
		}
		return u
	default:
		return 0
	}
}

// F64 returns a numeric field as float64; 0 otherwise.
func F64(v any, key string) float64 {
	n, _ := Get(v, key).(float64)
	return n
}

// F64Ptr returns a numeric field as float64 plus presence (null-safe).
func F64Ptr(v any, key string) (float64, bool) {
	raw := Get(v, key)
	if raw == nil {
		return 0, false
	}
	n, ok := raw.(float64)
	return n, ok
}

// Bool returns a boolean field; false otherwise.
func Bool(v any, key string) bool {
	b, _ := Get(v, key).(bool)
	return b
}

// NullField reports whether key is present with an explicit null value.
func NullField(v any, key string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	raw, ok := m[key]
	return ok && raw == nil
}

// Time parses common timestamp shapes: RFC3339 with optional fractional
// seconds, "2006-01-02 15:04:05", bare dates, and epoch seconds/millis (as a
// number or numeral string). Returns false for unusable input.
func Time(raw any) (int64, bool) {
	switch t := raw.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		if ms, ok := epochMillis(s); ok {
			return ms, true
		}
		for _, layout := range layouts {
			if ts, err := timeParse(layout, s); err == nil {
				return ts.UnixMilli(), true
			}
		}
		return 0, false
	case float64:
		ms := int64(t)
		if t > 1e14 { // already millis
			ms = int64(t)
		} else if t > 1e11 { // micros
			ms = int64(t) / 1000
		} else { // seconds
			ms = int64(t) * 1000
		}
		return ms, true
	default:
		return 0, false
	}
}

var layouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

func epochMillis(s string) (int64, bool) {
	if s[0] < '0' || s[0] > '9' {
		return 0, false
	}
	allDigits := true
	dots := 0
	for _, c := range s {
		if c == '.' {
			dots++
			continue
		}
		if c < '0' || c > '9' {
			allDigits = false
			break
		}
	}
	if !allDigits || dots > 1 {
		return 0, false
	}
	ms := U64Of(s)
	if ms <= 0 {
		return 0, false
	}
	v := int64(ms)
	if v > 1e14 {
		return v, true // millis
	}
	if v > 1e11 {
		return v / 1000, true // micros
	}
	return v * 1000, true // seconds
}

// CollectFiles walks each root recursively gathering files whose lowercase
// extension is in exts (with dot), optionally filtered by name. Missing roots
// are silently skipped — absence of data is normal. Results are sorted.
func CollectFiles(roots []string, exts []string, filter func(name string) bool) []string {
	want := map[string]bool{}
	for _, e := range exts {
		want[strings.ToLower(e)] = true
	}
	var out []string
	seenDir := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		if seenDir[dir] {
			return
		}
		seenDir[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, ent := range entries {
			p := filepath.Join(dir, ent.Name())
			if ent.IsDir() {
				walk(p)
				continue
			}
			if !ent.Type().IsRegular() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(ent.Name()))
			if !want[ext] {
				continue
			}
			if filter != nil && !filter(ent.Name()) {
				continue
			}
			out = append(out, p)
		}
	}
	for _, r := range roots {
		if fi, err := os.Stat(r); err == nil && fi.IsDir() {
			walk(r)
		}
	}
	sortStrings(out)
	return out
}

// ExistingDirs filters candidates to existing directories, de-duplicated.
func ExistingDirs(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// SplitEnv splits a comma-separated environment variable value, trimming
// whitespace and dropping empties.
func SplitEnv(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
