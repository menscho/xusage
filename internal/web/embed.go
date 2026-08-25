// Package web serves the embedded dashboard assets.
package web

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

//go:embed static
var staticFS embed.FS

const assetMaxAge = "max-age=300" // short window: assets are unhashed

// cacheControl is the full Cache-Control value for static assets, computed
// once so the per-request handler does not allocate concatenating it.
var cacheControl = "no-cache, " + assetMaxAge

// staticServer serves the embedded dashboard. Text assets are gzipped on first
// request and the compressed bytes reused, so the per-request CPU and
// allocations of streaming gzip disappear; the known length also lets us emit
// a real Content-Length instead of chunked encoding.
type staticServer struct {
	sub   fs.FS
	files http.Handler
	gz    sync.Map // name -> *gzCache
}

// gzCache holds the lazily-compressed bytes of one asset. once.Do compresses
// exactly once; concurrent requests for the same asset block only on the first.
type gzCache struct {
	once   sync.Once
	data   []byte
	failed bool
}

func (s *staticServer) gzipBytes(name string) ([]byte, bool) {
	c, _ := s.gz.LoadOrStore(name, &gzCache{})
	gc := c.(*gzCache)
	gc.once.Do(func() {
		raw, err := fs.ReadFile(s.sub, name)
		if err != nil {
			gc.failed = true
			return
		}
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		if err != nil {
			gc.failed = true
			return
		}
		if _, err := zw.Write(raw); err != nil {
			gc.failed = true
			return
		}
		if err := zw.Close(); err != nil {
			gc.failed = true
			return
		}
		gc.data = buf.Bytes()
	})
	if gc.failed {
		return nil, false
	}
	return gc.data, true
}

// Handler returns the dashboard's static file handler. It compresses
// text-like assets when the client accepts gzip, sets a short cache window
// (assets are unhashed), and falls back to index.html for unknown paths so
// hash routing works on deep links.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embed layout is fixed at build time
	}
	s := &staticServer{sub: sub, files: http.FileServer(http.FS(sub))}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orig := strings.TrimPrefix(r.URL.Path, "/") // classify gzip on what was asked for
		name := orig
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(sub, name); err != nil {
			name = "index.html" // SPA-style fallback to the shell
			r.URL.Path = "/"    // keep the file server on the same page
		}
		w.Header().Set("Cache-Control", cacheControl)
		w.Header().Set("Vary", "Accept-Encoding")
		// Serve pre-compressed bytes directly: the length is known, so we can
		// set a real Content-Length (the old streaming path could not — it had
		// to rely on chunked encoding to avoid truncating the body on the wire).
		// Classify on name (the resolved file), not the request path: the root
		// ("") and SPA deep links ("/foo/bar") both resolve to index.html and
		// must still be eligible for gzip.
		if r.Method == http.MethodGet && acceptsGzip(r) && wantsGzip(name) {
			if data, ok := s.gzipBytes(name); ok {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", contentTypeOf(name))
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				_, _ = w.Write(data)
				return
			} // cache miss / read failure: fall through to the plain file server
		}
		s.files.ServeHTTP(w, r)
	})
}

// contentTypeOf mirrors the file server's extension-based Content-Type.
func contentTypeOf(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// wantsGzip reports whether the asset compresses well enough to bother.
func wantsGzip(path string) bool {
	switch {
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".css"),
		strings.HasSuffix(path, ".html"), strings.HasSuffix(path, ".svg"),
		strings.HasSuffix(path, ".json"):
		return true
	}
	return false
}
