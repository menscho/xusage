package claude

import (
	"bufio"
	"os"

	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "claude",
		Discover: discover,
		Parse:    Parse,
	})
}

// discover returns every Claude transcript file, or (nil, nil) when no data
// directory exists.
func discover() ([]string, error) {
	return usageFiles()
}

// Parse reads one JSONL transcript, emitting an entry per valid usage line.
// Malformed lines are skipped; only unreadable files produce an error.
// Exported so cross-package tests can run fixtures through the real adapter.
func Parse(path string) ([]model.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	project := extractProject(path)
	var entries []model.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		// sc.Bytes() returns the scanner's internal buffer, reused on every
		// scan. parseLineBytes fully decodes the line before returning (no
		// retained slice references), so passing it directly skips a per-line
		// []byte(line) allocation that sc.Text() + string-to-bytes would do.
		parsed, ok := parseLineBytes(sc.Bytes(), path)
		if !ok {
			continue
		}
		for i := range parsed {
			parsed[i].Project = project
		}
		entries = append(entries, parsed...)
	}
	return entries, nil
}
