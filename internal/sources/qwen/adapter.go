// Package qwen — registry wiring.
package qwen

import (
	"os"

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

// osStat is a tiny indirection so tests can shadow it if ever needed.
func osStat(path string) (os.FileInfo, error) { return os.Stat(path) }
