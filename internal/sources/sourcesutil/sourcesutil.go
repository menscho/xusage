// Package sourcesutil holds helpers shared by the source adapters: environment
// overrides, home-relative defaults, file walking, and timestamp parsing.
//
// The adapters mirror the discovery and parsing behavior of ccusage's Rust
// implementations (rust/adapters/<name>/src/{paths.rs,parser.rs,loader.rs});
// this package centralizes the mechanics they all repeat.
package sourcesutil

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EnvPaths reads a comma- (or path-list-) separated environment override and
// returns its non-empty trimmed entries. An unset or empty variable yields nil.
func EnvPaths(name string) []string {
	raw := os.Getenv(name)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == os.PathListSeparator
	}) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// HomeJoin joins parts under the user's home directory. The second result is
// false when the home directory cannot be determined.
func HomeJoin(parts ...string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(append([]string{home}, parts...)...), true
}

// ExistingDirs keeps only the entries that are existing directories,
// preserving order and dropping duplicates.
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

// ExistingFiles keeps only the entries that are existing regular files,
// preserving order and dropping duplicates.
func ExistingFiles(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// WalkFiles collects files below every root for which match reports true,
// returning sorted, deduplicated absolute paths. Missing roots are skipped
// silently; discovery never fails on absent data.
func WalkFiles(roots []string, match func(path, name string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !match(path, d.Name()) {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				abs = path
			}
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// FilesWithExt collects every file with the given extension (with or without
// a leading dot) below the root, sorted. A missing root yields nil.
func FilesWithExt(root, ext string) []string {
	ext = strings.TrimPrefix(ext, ".")
	return WalkFiles([]string{root}, func(_, name string) bool {
		return strings.EqualFold(filepath.Ext(name), "."+ext)
	})
}

// LineReader feeds the lines of a file to visit, skipping read errors at the
// line level. A missing file is not an error: it is simply not read.
func LineReader(path string, visit func(line []byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing/unreadable file: nothing to parse, never fatal
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			visit(trimmed)
		}
	}
	return nil
}

// Contains reports whether line contains the substring, the fast prefilter
// adapters apply before paying for a JSON decode.
func Contains(line []byte, sub string) bool {
	return bytes.Contains(line, []byte(sub))
}

// Unmarshal decodes one JSON line into v, reporting whether it succeeded.
func Unmarshal(line []byte, v any) bool {
	return json.Unmarshal(line, v) == nil
}

// ParseTime parses the timestamp formats found in coding-agent logs: strict
// RFC3339 with optional millisecond precision (the dominant upstream shape,
// e.g. "2025-01-02T03:04:05.678Z"), plain RFC3339, unix seconds, and unix
// milliseconds. It reports false when nothing matches.
func ParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if n := len(s); (n == 20 || n == 25) && (s[19] == 'Z' || s[19] == '+' || s[19] == '-') {
		// "2006-01-02T15:04:05Z" style with second precision.
		if t, err := time.Parse("2006-01-02T15:04:05Z07:00", s); err == nil {
			return t, true
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.000Z07:00", // RFC3339 with millis (upstream canonical)
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// TimeFromNumber interprets a numeric timestamp: magnitudes above 1e12 are
// unix milliseconds, above 1e9 seconds, below that microseconds-of-day values
// are rejected and the number is treated as seconds. The bool is false for
// non-positive input.
func TimeFromNumber(n int64) (time.Time, bool) {
	if n <= 0 {
		return time.Time{}, false
	}
	switch {
	case n > 1e15: // microseconds
		return time.UnixMicro(n).UTC(), true
	case n > 1e12: // milliseconds
		return time.UnixMilli(n).UTC(), true
	default: // seconds
		return time.Unix(n, 0).UTC(), true
	}
}

// ModTime returns the file's modification time, or the zero time when the
// file cannot be stat'ed. Adapters use it as a last-resort timestamp.
func ModTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UTC()
	}
	return time.Time{}
}

// FirstNonEmpty returns the first non-blank string, or "".
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// BaseName returns the last path element of a slash- or platform-separated
// path, useful for turning cwd values into project names.
func BaseName(p string) string {
	if p == "" {
		return ""
	}
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// URLDecode decodes percent-encoded path segments (e.g. "C%3A%5Cproj"),
// falling back to the input when decoding fails.
func URLDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	if dec, err := url.PathUnescape(s); err == nil && dec != "" {
		return dec
	}
	return s
}

// JSONString extracts a non-empty string field from a generic decoded object.
func JSONString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// JSONNumber extracts a non-negative integer field from a generic decoded
// object, accepting floats that carry an integral value.
func JSONNumber(m map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			if v > 0 {
				return uint64(v)
			}
			return 0
		case json.Number:
			if n, err := v.Int64(); err == nil && n > 0 {
				return uint64(n)
			}
		}
	}
	return 0
}

// JSONObject extracts a nested object field.
func JSONObject(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if o, ok := m[k].(map[string]any); ok {
			return o
		}
	}
	return nil
}

// ApplyTotalFallback ports ccusage's apply_total_token_fallback: when a
// provider-reported total exceeds the sum of the known buckets, the missing
// remainder lands in OutputTokens if that bucket is empty, otherwise in
// extraTotal. It returns the adjusted buckets and extra count.
func ApplyTotalFallback(input, output, cacheCreate, cacheRead, extraTotal, total uint64) (in, out, cc, cr, extra uint64) {
	in, out, cc, cr, extra = input, output, cacheCreate, cacheRead, extraTotal
	known := in + out + cc + cr + extra
	if total <= known {
		return
	}
	missing := total - known
	if out == 0 {
		out = missing
		return
	}
	extra += missing
	return
}

// DeterministicRand seeds a *rand.Rand for reproducible synthetic data.
func DeterministicRand(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}
