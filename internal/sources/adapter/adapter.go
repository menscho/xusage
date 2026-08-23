// Package adapter holds helpers shared by the per-source adapters: JSONL
// scanning, timestamp parsing, and discovery primitives. It exists so each
// adapter package stays small; nothing here is used outside internal/sources.
package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CollectFiles walks root recursively and appends every regular file whose
// name ends with ext (with or without the leading dot). A missing or
// unreadable root is not an error — it simply yields no files.
func CollectFiles(root, ext string) []string {
	if ext != "" && ext[0] != '.' {
		ext = "." + ext
	}
	var files []string
	collect(root, ext, &files)
	return files
}

func collect(root, ext string, out *[]string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() {
			collect(p, ext, out)
			continue
		}
		if !e.Type().IsRegular() {
			continue // skip symlinks and oddities
		}
		if ext == "" || strings.HasSuffix(strings.ToLower(e.Name()), strings.ToLower(ext)) {
			*out = append(*out, p)
		}
	}
}

// SortStrings sorts paths in place so discovery output is deterministic.
func SortStrings(s []string) { sort.Strings(s) }

// HomeDir returns os.UserHomeDir, mapping failure to "" so callers can treat a
// missing home like any other absent default.
func HomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// ExistingDirs resolves raw (a comma-separated list of directories) against an
// optional home-relative default when raw is empty. Only existing directories
// are returned, deduplicated in order.
func ExistingDirs(raw string, defaults ...string) []string {
	var candidates []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			candidates = append(candidates, part)
		}
	}
	if len(candidates) == 0 {
		candidates = defaults
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		fi, err := os.Stat(c)
		if err != nil || !fi.IsDir() || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// Env returns the value of the environment variable with surrounding spaces
// trimmed; empty values read as unset.
func Env(name string) string { return strings.TrimSpace(os.Getenv(name)) }

// ParseTimestamp parses the timestamp formats seen across coding-agent logs:
// RFC3339 with fractional seconds, plain RFC3339, "2006-01-02 15:04:05", and
// date-only. The zero time is returned when nothing matches.
func ParseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z0700",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// TimestampFromMillis converts Unix milliseconds to a UTC time. Values at or
// below zero yield the zero time.
func TimestampFromMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// ReadLines streams the file line by line, calling fn for each non-empty line.
// Read errors stop the scan silently: partial data beats no data for usage logs.
func ReadLines(path string, fn func(line string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			fn(line)
		}
	}
	return nil
}

// UnmarshalObject decodes one JSON object into dst, rejecting lines that are
// not objects (malformed lines are skipped by callers).
func UnmarshalObject(line string, dst any) bool {
	return json.Unmarshal([]byte(line), dst) == nil
}

// BaseName returns the final path element without its extension.
func BaseName(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[:i]
	}
	return base
}

// FormatID renders t as the compact UTC ID used for block identifiers.
func FormatID(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// FloorToHour truncates t to the start of its hour.
func FloorToHour(t time.Time) time.Time {
	return t.Truncate(time.Hour).UTC()
}

// StringPtr / Float64Ptr / TimePtr build pointers for optional fields.
func StringPtr(s string) *string     { return &s }
func Float64Ptr(f float64) *float64  { return &f }
func TimePtr(t time.Time) *time.Time { return &t }
func Int64Ptr(i int64) *int64        { return &i }
func Uint64Ptr(u uint64) *uint64     { return &u }

// HomeDirs resolves raw (comma-separated directories, ~ expanded) against
// home-relative defaults when raw is empty. Only existing directories are
// returned, deduplicated in order.
func HomeDirs(raw string, defaults ...string) []string {
	var candidates []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "~") {
			if home := HomeDir(); home != "" {
				part = filepath.Join(home, strings.TrimPrefix(part, "~"))
			}
		}
		candidates = append(candidates, part)
	}
	if len(candidates) == 0 {
		candidates = defaults
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		fi, err := os.Stat(c)
		if err != nil || !fi.IsDir() || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// ReadLines2 streams the file line by line into fn. It is ReadLines with a
// signature that lets callers distinguish "file missing" from "no lines":
// a missing file yields no error, matching the sources contract that absent
// data is normal.
func ReadLines2(path string, fn func(line string)) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return ReadLines(path, fn)
}

// FirstNonEmpty returns the first argument that is non-empty after trimming,
// or "" when every argument is empty.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

var _ = fmt.Sprintf // keep fmt available for adapters importing this file's siblings
