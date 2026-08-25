// Package roocode adapts Roo Code (roo-cline) task transcripts to the common
// source registry.
//
// Upstream ccusage reads Roo Code's per-task JSONL transcripts from VS Code's
// globalStorage (the roo-cline extension namespace). This adapter mirrors that
// discovery and parses the same message payloads. See docs/NOTES.md.
package roocode

import (
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "roocode",
		Discover: discover,
		Parse:    parseFile,
	})
}
