// Package goose adapts the Goose coding agent's session store for xusage.
//
// Upstream ccusage reads goose's SQLite database (sessions.db, located via
// GOOSE_PATH_ROOT or the platform data dirs). xusage is stdlib-only and ships
// no SQLite driver, so this adapter parses the JSONL/JSON export subset of the
// same schema (see parser.go) while discovery mirrors the upstream path rules
// exactly (see paths.go).
package goose

import "xusage/internal/sources"

func init() {
	sources.Register(sources.Source{
		Name:     "goose",
		Discover: Discover,
		Parse:    Parse,
	})
}
