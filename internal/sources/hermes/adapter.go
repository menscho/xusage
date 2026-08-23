// Hermes source adapter: registers the "hermes" entry in the sources
// registry and maps state databases onto usage entries.
package hermes

import (
	"os"

	"xusage/internal/model"
	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "hermes",
		Discover: discover,
		Parse:    parse,
	})
}

// discover lists existing Hermes state database files. A missing data
// directory is normal and yields (nil, nil).
func discover() ([]string, error) {
	return stateDBPaths(os.UserHomeDir), nil
}

// parse extracts usage entries from one Hermes state database. Unreadable or
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
