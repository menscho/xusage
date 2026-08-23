// Package hermes adapts Hermes usage data (HERMES_HOME or ~/.hermes) into
// xusage entries.
//
// Upstream ccusage reads a SQLite state database (state.db, sessions table);
// this adapter mirrors that faithfully with a minimal read-only SQLite reader
// (see sqlite.go). One sessions row becomes one usage entry.
package hermes
