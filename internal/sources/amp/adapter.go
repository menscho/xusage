// Package amp registers the Amp source. See paths.go for discovery rules.
package amp

import (
	"os"
	"strconv"
	"time"

	"xusage/internal/sources"
)

func init() {
	sources.Register(sources.Source{
		Name:     "amp",
		Discover: discover,
		Parse:    parse,
	})
}

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func msTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
