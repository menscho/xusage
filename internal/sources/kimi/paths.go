// Package kimi parses Kimi / Kimi Code wire logs (JSONL) into usage entries.
//
// Discovery mirrors upstream ccusage: the KIMI_DATA_DIR environment variable
// (comma-separated, wins over defaults) or ~/.kimi and ~/.kimi-code. Only
// sessions/**/wire.jsonl files are considered, in either the old
// (sessions/<group>/<session>/) or new (sessions/<ws>/<session>/agents/<agent>/)
// layout.
package kimi

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	dataDirEnv    = "KIMI_DATA_DIR"
	sessionsDir   = "sessions"
	wireFileName  = "wire.jsonl"
	defaultHome   = ".kimi"
	altHome       = ".kimi-code"
	agentsDirName = "agents"
)

// dataDirs returns the kimi data roots to scan. KIMI_DATA_DIR (comma-separated
// list) wins over the home-relative defaults. Missing directories are normal
// and simply omitted.
func dataDirs() []string {
	if raw := strings.TrimSpace(os.Getenv(dataDirEnv)); raw != "" {
		var dirs []string
		seen := map[string]bool{}
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if fi, err := os.Stat(part); err != nil || !fi.IsDir() {
				continue
			}
			abs, err := filepath.Abs(part)
			if err != nil {
				abs = part
			}
			if !seen[abs] {
				seen[abs] = true
				dirs = append(dirs, abs)
			}
		}
		return dirs
	}
	home, err := os.UserHomeDir()
	if err == nil || home != "" {
		var dirs []string
		seen := map[string]bool{}
		for _, name := range []string{defaultHome, altHome} {
			p := filepath.Join(home, name)
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
				continue
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				abs = p
			}
			if !seen[abs] {
				seen[abs] = true
				dirs = append(dirs, abs)
			}
		}
		return dirs
	}
	return nil
}

// isWireFile reports whether path is a wire.jsonl in one of the two known
// session layouts below root/sessions:
//
//	old: sessions/<group>/<session>/wire.jsonl              (3 components)
//	new: sessions/<ws>/<session>/agents/<agent>/wire.jsonl  (5 components)
func isWireFile(sessionsRoot, path string) bool {
	if filepath.Base(path) != wireFileName {
		return false
	}
	rel, err := filepath.Rel(sessionsRoot, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	n := len(strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' }))
	return n == 3 || n == 5
}

// Discover returns every wire.jsonl file under the detected data roots.
// No kimi directory yields (nil, nil) — absence is not an error.
func Discover() ([]string, error) {
	var files []string
	seen := map[string]bool{}
	for _, root := range dataDirs() {
		sessionsRoot := filepath.Join(root, sessionsDir)
		fi, err := os.Stat(sessionsRoot)
		if err != nil || !fi.IsDir() {
			continue
		}
		_ = filepath.WalkDir(sessionsRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip, never fail discovery
			}
			if d.IsDir() || !isWireFile(sessionsRoot, path) {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				abs = path
			}
			if !seen[abs] {
				seen[abs] = true
				files = append(files, abs)
			}
			return nil
		})
	}
	sortStrings(files)
	return files, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
