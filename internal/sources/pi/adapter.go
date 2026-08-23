package pi

import "xusage/internal/sources"

// Discover returns every pi session .jsonl file path. A missing data
// directory is normal and yields (nil, nil).
func Discover() ([]string, error) {
	return discoverJSONL(sessionDirs()), nil
}

func init() {
	sources.Register(sources.Source{
		Name:     "pi",
		Discover: Discover,
		Parse:    Parse,
	})
}
