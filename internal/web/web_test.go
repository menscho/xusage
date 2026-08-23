package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerServesCoreAssets guards the embed against missing shell files:
// every file the dashboard needs to boot must be present in static/.
func TestHandlerServesCoreAssets(t *testing.T) {
	for _, path := range []string{
		"/index.html",
		"/",
		"/css/style.css",
		"/js/app.js",
	} {
		t.Run(path, func(t *testing.T) {
			srv := httptest.NewServer(Handler())
			defer srv.Close()
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s status = %d, want 200", path, resp.StatusCode)
			}
			if strings.Contains(path, ".css") && !strings.Contains(resp.Header.Get("Content-Type"), "text/css") {
				t.Errorf("GET %s content-type = %q, want text/css", path, resp.Header.Get("Content-Type"))
			}
		})
	}
}

// TestHandlerFallbackToIndex checks SPA-style fallback: an unknown asset path
// must still serve the shell rather than a 404.
func TestHandlerFallbackToIndex(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/js/does_not_exist.js")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("fallback status = %d, want 200", resp.StatusCode)
	}
}
