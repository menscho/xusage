package kimi

import "xusage/internal/sources"

// wireSourceName is the registry name for this adapter.
const wireSourceName = "kimi"

func init() {
	sources.Register(sources.Source{
		Name:     wireSourceName,
		Discover: Discover,
		Parse:    Parse,
	})
}
