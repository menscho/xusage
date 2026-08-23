// Package claude adapts Claude Code's local JSONL transcript logs
// ($CLAUDE_CONFIG_DIR/projects/**/*.jsonl) into xusage entries.
package claude

import (
	"os"
	"path/filepath"
	"strings"
)

// discoverDirs resolves the Claude config directories holding a projects/
// subtree. CLAUDE_CONFIG_DIR (comma-separated) wins over the XDG and ~/.claude
// defaults, mirroring upstream claude_paths.
func discoverDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if !hasProjects(dir) || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if raw := os.Getenv("CLAUDE_CONFIG_DIR"); strings.TrimSpace(raw) != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			add(normalizeConfigDir(part))
		}
		return dirs
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".claude"))
		// Upstream also checks <xdg>/claude, where <xdg> defaults to
		// ~/.config when XDG_CONFIG_HOME is unset.
		xdg := filepath.Join(home, ".config")
		if env := os.Getenv("XDG_CONFIG_HOME"); strings.TrimSpace(env) != "" {
			xdg = env
		}
		add(filepath.Join(xdg, "claude"))
	}
	return dirs
}

// normalizeConfigDir accepts either the config directory itself or its
// projects/ subdirectory, mapping the latter onto the parent.
func normalizeConfigDir(raw string) string {
	p := expandHome(raw)
	if strings.EqualFold(filepath.Base(p), "projects") {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return filepath.Dir(p)
		}
	}
	return p
}

func hasProjects(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "projects"))
	return err == nil && fi.IsDir()
}

// expandHome resolves a leading ~ or ~\ to the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), string(filepath.Separator)))
		}
	}
	return p
}

// usageFiles returns every .jsonl transcript below each config dir's projects
// subtree, sorted for stable load order.
func usageFiles() ([]string, error) {
	return usageFilesIn(discoverDirs()...)
}

// usageFilesIn collects .jsonl files under each config dir's projects subtree.
func usageFilesIn(dirs ...string) ([]string, error) {
	var files []string
	for _, dir := range dirs {
		root := filepath.Join(dir, "projects")
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtrees are skipped, not fatal
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".jsonl") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
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

// splitComponents normalizes to forward slashes and splits the path.
func splitComponents(path string) []string {
	return strings.Split(filepath.ToSlash(path), "/")
}

// extractProject returns the first path segment under "projects",
// or "unknown" when the layout does not match.
func extractProject(path string) string {
	parts := splitComponents(path)
	for i, part := range parts {
		if part == "projects" && i+1 < len(parts) {
			if strings.TrimSpace(parts[i+1]) == "" {
				return "unknown"
			}
			return parts[i+1]
		}
	}
	return "unknown"
}

// extractSessionParts derives (sessionID, projectPath) from the transcript
// path layout, mirroring upstream extract_session_parts:
//
//	projects/p/s.jsonl                      -> (s, p)
//	projects/p/s/chat.jsonl                 -> (s, p)
//	projects/p/s/subagents/w.jsonl          -> (w's parent session, p)
func extractSessionParts(path string) (sessionID, projectPath string) {
	parts := splitComponents(path)
	start := 0
	for i, part := range parts {
		if part == "projects" {
			start = i + 1
			break
		}
	}
	rel := parts[start:]
	if len(rel) == 0 {
		return "unknown", "Unknown Project"
	}
	if stem := strings.TrimSuffix(rel[len(rel)-1], ".jsonl"); stem != "" && stem != rel[len(rel)-1] && len(rel) == 2 {
		return stem, rel[0]
	}
	if len(rel) >= 4 && rel[len(rel)-2] == "subagents" {
		session := rel[len(rel)-3]
		project := strings.Join(rel[:len(rel)-3], "/")
		if project == "" {
			project = "Unknown Project"
		}
		return session, project
	}
	session := "unknown"
	if len(rel) >= 2 {
		session = rel[len(rel)-2]
	}
	project := "Unknown Project"
	if len(rel) > 2 {
		project = strings.Join(rel[:len(rel)-2], "/")
	}
	return session, project
}
