// Package grok adapts the Grok CLI's local session logs into xusage usage
// entries. Data lives under $GROK_HOME (or ~/.grok) as one updates.jsonl per
// session directory, optionally accompanied by a summary.json carrying the
// canonical session id, working directory, and default model.
package grok

import (
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "grok",
		Discover: discoverSessionFiles,
		Parse:    parseFile,
	})
}
