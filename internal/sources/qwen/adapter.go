// Package qwen — registry wiring.
package qwen

import (
	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "qwen",
		Discover: discover,
		Parse:    parse,
	})
}

func discover() ([]string, error) {
	return discoverChatFiles(), nil
}

func parse(path string) ([]model.Entry, error) {
	return parseFile(path), nil
}
