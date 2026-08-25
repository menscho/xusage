// Package augment — registry wiring.
package augment

import (
	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "augment",
		Discover: discover,
		Parse:    parseFile,
	})
}

func parse(path string) ([]model.Entry, error) {
	return parseFile(path)
}
