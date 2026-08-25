// Package server — /api/providers and /api/models handlers for the
// redesigned attribution views (vendor rollups, model details with trends).
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"

	"xusage/internal/engine"
)

// qCache caches serialized JSON responses for the vendor/model endpoints,
// keyed by snapshot identity + query string. Snapshot identity is the
// invalidation signal: a new snapshot (after a refresh) orphans every
// previous entry at once, bounding memory to the unique queries asked
// against whichever snapshot is live (a handful of small JSON bodies).
//
// Caching the serialized bytes — not the Go result — skips both the
// per-request aggregation and the JSON encoding on a hit. Those are the
// two largest allocation sources for these endpoints: filtered() builds a
// fresh []*model.Entry slice (~800KB per 100k entries) and the group maps
// on every call, and writeJSON allocates a fresh json.Encoder each time.
// The engine's trendCache already memoizes Trend7; qCache lifts that to
// the full response so a steady-state poll pays neither aggregation nor
// serialization.
type qCache struct {
	mu   sync.Mutex
	snap *engine.Snapshot
	body map[string][]byte // "p:/m:..." prefix + encoded query -> JSON bytes
}

// vendorsQCacheCap bounds the number of distinct query keys cached per
// snapshot. The dashboard issues a handful of fixed queries, so this never
// triggers in practice — it only guards against a pathological client sending
// ever-unique query strings within one snapshot (which would otherwise grow
// the map without bound). On overflow the map is reset; the next request
// repopulates the working set.
const vendorsQCacheCap = 64

var vendorsQCache = &qCache{body: map[string][]byte{}}

func (c *qCache) get(snap *engine.Snapshot, key string) ([]byte, bool) {
	c.mu.Lock()
	if c.snap != snap {
		c.snap = snap
		c.body = map[string][]byte{}
	}
	b, ok := c.body[key]
	c.mu.Unlock()
	return b, ok
}

func (c *qCache) put(snap *engine.Snapshot, key string, b []byte) {
	c.mu.Lock()
	if c.snap == snap {
		if len(c.body) >= vendorsQCacheCap {
			c.body = map[string][]byte{}
		}
		c.body[key] = b
	}
	c.mu.Unlock()
}

// encodeJSON serializes v to JSON bytes with the same settings as writeJSON
// (HTML-escaping disabled) so cached bytes are byte-identical to a fresh
// encoding and can be written straight to the response on a hit.
func encodeJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.Bytes()
}

// writeCachedJSON writes a pre-encoded JSON body with the standard API
// headers (Content-Type + no-store). Used for qCache hits to skip both
// aggregation and encoding.
func writeCachedJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func handleProviders(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	q := r.URL.Query()
	key := "p:" + q.Encode()
	if body, ok := vendorsQCache.get(snap, key); ok {
		writeCachedJSON(w, body)
		return
	}
	// Parse once: the cache key already consumed q, so feed optsFromQuery the
	// same url.Values instead of re-parsing r.URL.Query() for Opts. On a miss
	// this drops one url.ParseQuery map allocation. (Hit path never builds Opts.)
	body := encodeJSON(snap.ByVendor(optsFromQuery(q)))
	vendorsQCache.put(snap, key, body)
	writeCachedJSON(w, body)
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	snap := engine.Get()
	q := r.URL.Query()
	trend := q.Get("trend") == "1"
	key := "m:" + q.Encode()
	if body, ok := vendorsQCache.get(snap, key); ok {
		writeCachedJSON(w, body)
		return
	}
	// Reuse q parsed above for the cache key instead of re-parsing for Opts.
	body := encodeJSON(snap.ModelsDetailed(optsFromQuery(q), trend))
	vendorsQCache.put(snap, key, body)
	writeCachedJSON(w, body)
}
