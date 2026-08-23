// Package opencode — registry wiring for the OpenCode adapter.
package opencode

import "xusage/internal/sources"

// init registers the OpenCode source with the global adapter registry when
// the package is linked in (blank-imported from internal/sources/all).
func init() {
	sources.Register(sources.Source{
		Name:     "opencode",
		Discover: discover,
		Parse:    parseFile,
	})
}
