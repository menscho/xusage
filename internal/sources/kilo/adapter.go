package kilo

import (
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "kilo",
		Discover: discover,
		Parse:    parseFile,
	})
}
