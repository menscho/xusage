package qwen

import (
	"os"
	"path/filepath"
	"strings"
)

// dataDirEnv is the Qwen data-directory override.
const dataDirEnv = "QWEN_DATA_DIR"

// dataDirs returns the Qwen data roots to scan. QWEN_DATA_DIR (comma-separated)
// wins over the home default, mirroring upstream paths(). Absent directories
// are normal and simply drop out.
func dataDirs() []string {
	var candidates []string
	if raw := strings.TrimSpace(os.Getenv(dataDirEnv)); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				candidates = append(candidates, part)
			}
		}
	} else if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".qwen"))
	}

	seen := map[string]bool{}
	var out []string
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

// discoverChatFiles collects projects/<project>/chats/*.jsonl under every data
// root. Only that exact three-segment layout counts as a chat file, matching
// upstream is_chat_file.
func discoverChatFiles() []string {
	var files []string
	for _, root := range dataDirs() {
		projects := filepath.Join(root, "projects")
		entries, err := os.ReadDir(projects)
		if err != nil {
			continue
		}
		for _, proj := range entries {
			if !proj.IsDir() {
				continue
			}
			chats := filepath.Join(projects, proj.Name(), "chats")
			chatEntries, err := os.ReadDir(chats)
			if err != nil {
				continue
			}
			for _, f := range chatEntries {
				if !f.Type().IsRegular() {
					continue
				}
				if strings.HasSuffix(strings.ToLower(f.Name()), ".jsonl") {
					files = append(files, filepath.Join(chats, f.Name()))
				}
			}
		}
	}
	sortStrings(files)
	return files
}

// projectFromFile returns the project segment of a chats path: the directory
// two levels above the file. Empty when the layout does not match.
func projectFromFile(path string) string {
	dir := filepath.Dir(path) // .../projects/<project>/chats
	chats := filepath.Base(dir)
	if chats != "chats" {
		return ""
	}
	projectDir := filepath.Dir(dir)
	project := filepath.Base(projectDir)
	if project == "." || project == string(filepath.Separator) {
		return ""
	}
	return project
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
