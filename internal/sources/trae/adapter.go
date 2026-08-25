// Package trae — registry wiring.
package trae

import (
	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "trae",
		Discover: discover,
		Parse:    parseFile,
	})
}

func parse(path string) ([]model.Entry, error) {
	return parseFile(path)
}
