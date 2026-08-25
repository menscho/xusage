// Package adapter holds helpers shared by the per-source adapters: JSONL
// scanning, timestamp parsing, and discovery primitives. It exists so each
// adapter package stays small; nothing here is used outside internal/sources.
package adapter

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// discoveryCacheTTL bounds how long a cached discovery survives without a
// full re-walk. The root-mtime sentinel handles the common case (unchanged
// tree -> skip the walk); this backstop guarantees a nested addition is
// noticed even when an ancestor dir's mtime happens to match, and prevents
// an atrophied entry from living forever. Tuned well above the 2s poll
// interval so steady-state polls never re-walk.
const discoveryCacheTTL = 30 * time.Second

// discoveryCacheMax bounds the number of distinct (root, ext) keys the
// discovery cache may hold. Real sources resolve to a handful of stable
// directories, so this is a defensive ceiling: if ever exceeded (e.g. a
// misconfigured env var producing a new root every poll) the cache is
// reset on the next insert so it cannot grow without bound.
const discoveryCacheMax = 64

// discoveryKey identifies one cached CollectFiles result. A struct key
// avoids the per-call string-concat allocation that a "root\x00ext" key
// would otherwise require on every poll, and cannot collide the way a
// joined string could (("a","bc") vs ("ab","c")).
type discoveryKey struct {
	root string
	ext  string
}

// discoveryEntry is one cached CollectFiles result plus the root-mtime
// sentinel and the time it was stored (for the TTL backstop). The files
// slice is cap-pinned (len == cap) so a caller that appends to it cannot
// scribble on the cache's backing array.
type discoveryEntry struct {
	files  []string
	mtime  time.Time
	stored time.Time
}

var (
	discoveryMu    sync.Mutex
	discoveryCache = map[discoveryKey]discoveryEntry{}
)

// interned holds process-wide interned strings. Model and provider names
// repeat across thousands of entries (e.g. one "claude-sonnet-4-..." string
// per entry in a large transcript); interning shares a single backing array
// per distinct value. The set of distinct names across all providers and
// history is small and bounded (low hundreds), so this map does not grow
// without limit. Lookups dominate writes once warm, but a plain Mutex keeps
// the lookup hot path smaller than a sync.Map for a map this small.
var (
	internMu sync.Mutex
	interned = map[string]string{}
)

// Intern returns a shared instance of s: the first call for a given value
// records it and every later call with an equal string returns the same
// pointer. Intern only strings you know are long-lived and repeatedly seen
// (model/provider names); do not intern high-cardinality values like ids or
// raw line content. Safe for concurrent use from the engine's parallel parse
// workers.
func Intern(s string) string {
	if s == "" {
		return ""
	}
	internMu.Lock()
	if g, ok := interned[s]; ok {
		internMu.Unlock()
		return g
	}
	interned[s] = s
	internMu.Unlock()
	return s
}

// CollectFiles walks root recursively and appends every regular file whose
// name ends with ext (with or without the leading dot). A missing or
// unreadable root is not an error — it simply yields no files.
//
// Results are memoized per (root, ext): a poll whose root directory mtime is
// unchanged since the last walk returns the cached list instead of
// re-reading the tree, so the 2s discovery tick costs a single os.Stat in
// steady state. The TTL eventually forces a re-walk to bound staleness.
func CollectFiles(root, ext string) []string {
	if ext != "" && ext[0] != '.' {
		ext = "." + ext
	}
	if ext != "" {
		ext = strings.ToLower(ext)
	}
	key := discoveryKey{root: root, ext: ext}

	fi, statErr := os.Stat(root)
	var mtime time.Time
	if statErr == nil {
		mtime = fi.ModTime().UTC()
	}

	discoveryMu.Lock()
	if e, ok := discoveryCache[key]; ok && time.Since(e.stored) < discoveryCacheTTL && e.mtime.Equal(mtime) {
		discoveryMu.Unlock()
		return e.files
	}
	discoveryMu.Unlock()

	var files []string
	collect(root, ext, &files)
	files = files[:len(files):len(files)] // pin cap: append-safe cache line

	discoveryMu.Lock()
	if len(discoveryCache) >= discoveryCacheMax {
		// Pathological growth (e.g. a new root every poll): reset rather
		// than accumulate without bound. The next poll repopulates.
		discoveryCache = map[discoveryKey]discoveryEntry{}
	}
	discoveryCache[key] = discoveryEntry{files: files, mtime: mtime, stored: time.Now()}
	discoveryMu.Unlock()
	return files
}

func collect(root, ext string, out *[]string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(root, name)
		if e.IsDir() {
			collect(p, ext, out)
			continue
		}
		if !e.Type().IsRegular() {
			continue // skip symlinks and oddities
		}
		if ext == "" || hasSuffixCI(name, ext) {
			*out = append(*out, p)
		}
	}
}

// hasSuffixCI reports whether name ends with ext, ignoring case. ext is
// assumed already lowercased (CollectFiles normalizes it before calling
// collect). The tail slice shares name's backing array, and EqualFold
// compares without allocating — so this avoids the per-file string copy
// that strings.ToLower(name) would otherwise require.
func hasSuffixCI(name, ext string) bool {
	if len(name) < len(ext) {
		return false
	}
	return strings.EqualFold(name[len(name)-len(ext):], ext)
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

// timestampLayouts are the timestamp formats seen across coding-agent logs,
// tried in order. Package-level so the per-call range allocation (one slice
// per parsed line) is paid once at init; the slice is read-only afterward and
// safe for concurrent use.
var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02",
}

// ParseTimestamp parses one of the timestamp formats seen across coding-agent
// logs: RFC3339 with fractional seconds, plain RFC3339, "2006-01-02 15:04:05",
// and date-only. The zero time is returned when nothing matches.
func ParseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range timestampLayouts {
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
