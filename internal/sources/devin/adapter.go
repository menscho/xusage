// Package devin adapts Devin CLI session databases to the common source
// registry.
//
// Upstream ccusage reads Devin CLI's SQLite sessions database
// (~/.local/share/devin/cli/sessions.db, see upstream rust/adapters/devin).
// xusage ships no SQLite driver, so this package carries a minimal pure-Go
// reader (sqlite.go) for the on-disk format and maps the sessions table onto
// usage entries. See docs/NOTES.md.
package devin

import (
	"os"

	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "devin",
		Discover: discover,
		Parse:    parse,
	})
}

// parse extracts usage entries from one Devin sessions database. Unreadable or
// foreign files yield zero entries rather than a failed load; only I/O errors
// reading the file itself are reported.
func parse(path string) ([]model.Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	db, err := openSQLite(data)
	if err != nil {
		return nil, nil // unreadable formats are skipped, not fatal
	}
	ti, err := db.findTable("sessions")
	if err != nil || ti == nil {
		return nil, nil // no sessions table: nothing to read
	}
	var entries []model.Entry
	if err := db.rows(ti, func(rowid int64, vals []any) error {
		r := newSessionRow(ti.columns, vals, rowid)
		if e, ok := r.entry(); ok {
			entries = append(entries, e)
		}
		return nil
	}); err != nil {
		return nil, nil // corrupt b-trees degrade to whatever parsed cleanly
	}
	return entries, nil
}
