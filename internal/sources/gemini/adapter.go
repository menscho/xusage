// Package gemini — registry wiring.
package gemini

import (
	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "gemini",
		Discover: discover,
		Parse:    parse,
	})
}

func discover() ([]string, error) {
	return discoverLogFiles(), nil
}

func parse(path string) ([]model.Entry, error) {
	return parseFile(path), nil
}
