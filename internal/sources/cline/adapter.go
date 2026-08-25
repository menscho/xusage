package cline

import (
	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "cline",
		Discover: discover,
		Parse:    parse,
	})
}

func discover() ([]string, error) {
	return discoverFiles(), nil
}

func parse(path string) ([]model.Entry, error) {
	return parseFile(path)
}
