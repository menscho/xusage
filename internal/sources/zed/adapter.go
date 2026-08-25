// Package zed — registry wiring.
package zed

import (
	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "zed",
		Discover: discover,
		Parse:    parseFile,
	})
}

func parse(path string) ([]model.Entry, error) {
	return parseFile(path)
}
