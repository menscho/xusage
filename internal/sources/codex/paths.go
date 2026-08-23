// Package codex adapts OpenAI Codex CLI rollout logs (~/.codex/sessions and
// ~/.codex/archived_sessions) into xusage entries.
package codex

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoverDirs resolves the Codex home directories. CODEX_HOME (comma-
// separated) wins over the ~/.codex default, mirroring upstream.
func discoverDirs() []string {
	if raw := os.Getenv("CODEX_HOME"); strings.TrimSpace(raw) != "" {
		var dirs []string
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				dirs = append(dirs, part)
			}
		}
		return dirs
	}
	if home, err := os.UserHomeDir(); err == nil {
		return []string{filepath.Join(home, ".codex")}
	}
	return nil
}

// usageSource is one scanned directory plus the home it belongs to. The home
// scopes the relative-path dedup key: sessions/ and archived_sessions/ of the
// same home hold copies of the same rollouts, different homes do not.
type usageSource struct{ dir, home string }

// usageSources picks, per home, the sessions/ and archived_sessions/
// subtrees; when neither exists it falls back to scanning the home itself
// (headless exec logs live directly under it).
func usageSources() []usageSource {
	var sources []usageSource
	seen := map[string]bool{}
	add := func(dir, home string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		sources = append(sources, usageSource{dir, home})
	}
	for _, home := range discoverDirs() {
		found := false
		for _, name := range []string{"sessions", "archived_sessions"} {
			d := filepath.Join(home, name)
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				add(d, home)
				found = true
			}
		}
		if !found {
			if fi, err := os.Stat(home); err == nil && fi.IsDir() {
				add(home, home)
			}
		}
	}
	return sources
}

// usageDirs keeps the flat directory list (root lookup, tests).
func usageDirs() []string {
	var dirs []string
	for _, s := range usageSources() {
		dirs = append(dirs, s.dir)
	}
	return dirs
}

// usageFilesIn collects .jsonl files under explicit roots (test helper path).
func usageFilesIn(dirs ...string) ([]string, error) {
	var files []string
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".jsonl") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

// usageFiles returns every .jsonl rollout file below the usage dirs, sorted.
// A file whose relative path was already seen under an earlier source of the
// same home is skipped: sessions/ holds the active copy of a rollout that
// archived_sessions/ also contains (upstream keeps the active copy).
func usageFiles() ([]string, error) {
	var files []string
	seenRel := map[string]bool{}
	for _, src := range usageSources() {
		all, err := usageFilesIn(src.dir)
		if err != nil {
			return nil, err
		}
		for _, f := range all {
			rel, err := filepath.Rel(src.dir, f)
			if err != nil {
				rel = filepath.Base(f)
			}
			key := src.home + "\x00" + filepath.ToSlash(rel)
			if seenRel[key] {
				continue
			}
			seenRel[key] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files, nil
}

// sessionIDFor derives the session id from the rollout path relative to its
// sessions root: the slash-joined path without extension (upstream keeps the
// date hierarchy, e.g. 2026/05/12/rollout-...-abc.jsonl).
func sessionIDFor(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = strings.TrimSuffix(rel, filepath.Ext(rel))
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return "unknown"
	}
	return rel
}

// projectFromCWD reduces a working directory to its base name, matching how
// upstream names codex projects after the folder that was opened.
func projectFromCWD(cwd string) string {
	cwd = strings.TrimRight(filepath.ToSlash(strings.TrimSpace(cwd)), "/")
	if cwd == "" {
		return ""
	}
	if i := strings.LastIndexByte(cwd, '/'); i >= 0 && i+1 < len(cwd) {
		return cwd[i+1:]
	}
	return cwd
}
