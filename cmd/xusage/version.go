// Package main — version plumbing. The version variable lives in flags.go and
// is overridden at build time via -ldflags "-X main.version=...".
package main

import "fmt"

// fullVersion renders the version string used by -version.
func fullVersion() string { return fmt.Sprintf("xusage %s", version) }
