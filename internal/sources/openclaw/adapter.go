package openclaw

import (
	"os"

	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "openclaw",
		Discover: discover,
		Parse:    parse,
	})
}

// discover returns every OpenClaw session file across the data roots, or
// (nil, nil) when no data directory exists.
func discover() ([]string, error) {
	var files []string
	for _, dir := range discoverDirs() {
		found, err := sessionFilesIn(dir)
		if err != nil {
			continue // unreadable root = no files from it
		}
		files = append(files, found...)
	}
	return files, nil
}

// parse reads one session JSONL file, emitting an entry per assistant usage
// record. Malformed lines are skipped; only unreadable files produce an error.
func parse(path string) ([]model.Entry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSessionFile(path, content), nil
}
