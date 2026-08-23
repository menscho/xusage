// Package web serves the embedded dashboard assets.
package web

import (
	"compress/gzip"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed static
var staticFS embed.FS

const assetMaxAge = "max-age=300" // short window: assets are unhashed

// Handler returns the dashboard's static file handler. It compresses
// text-like assets when the client accepts gzip, sets a short immutable-style
// cache window (assets are unhashed), and falls back to index.html for
// unknown paths so hash routing works on deep links.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embed layout is fixed at build time
	}
	fileServer := http.FileServer(http.FS(sub))
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
		w.Header().Set("Cache-Control", "no-cache, "+assetMaxAge)
		w.Header().Add("Vary", "Accept-Encoding")
		// Gzip is served from the embedded bytes directly: routing the file
		// server through a gzip writer would let ServeContent announce the
		// plain Content-Length and truncate the compressed body on the wire.
		if r.Method == http.MethodGet && acceptsGzip(r) && wantsGzip(orig) {
			if data, err := fs.ReadFile(sub, name); err == nil {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", contentTypeOf(name))
				gz := gzip.NewWriter(w)
				_, _ = gz.Write(data)
				_ = gz.Close()
				return
			} // read failure: fall through to the plain file server
		}
		fileServer.ServeHTTP(w, r)
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
