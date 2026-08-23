package fixtures

import (
	"fmt"
)

// This file holds shared helpers for the fixture generators. The record
// renderers live in claude.go and codex.go; WriteCorpus lives in corpus.go.

// SampleCorpusDir is a convenience used by other packages' tests: it writes a
// default corpus into dir and returns the summary. Errors panic because a
// failed corpus write is always a test bug.
func SampleCorpusDir(dir string) CorpusInfo {
	info, err := WriteCorpus(dir, CorpusOpts{})
	if err != nil {
		panic(fmt.Sprintf("fixtures: writing sample corpus: %v", err))
	}
	return info
}
