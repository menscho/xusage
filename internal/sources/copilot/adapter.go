// Package copilot — registry wiring.
package copilot

import (
	"time"

	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "copilot",
		Discover: discover,
		Parse:    parse,
	})
}

func discover() ([]string, error) {
	return discoverFiles(), nil
}

func parse(path string) ([]model.Entry, error) {
	return parseFile(path), nil
}

func timeFromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
