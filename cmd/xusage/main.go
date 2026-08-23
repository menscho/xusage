// Command xusage serves a local dashboard over coding-agent usage logs.
//
// It discovers every registered source's data directory, parses and prices
// usage entries into an in-memory snapshot, and exposes the analytics API plus
// the embedded web UI on 127.0.0.1 only.
package main

import (
	_ "xusage/internal/sources/all" // link every source adapter into the binary
)

func main() {
	osExit(run())
}
