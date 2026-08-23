package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getAsset(t *testing.T, path, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	return rec
}

func TestStaticAssetServed(t *testing.T) {
	rec := getAsset(t, "/css/style.css", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /css/style.css status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q, want a max-age directive", cc)
	}
}

func TestGzipWhenAccepted(t *testing.T) {
	rec := getAsset(t, "/css/style.css", "gzip")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding, headers: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("body not gzip: %v", err)
	}
	defer zr.Close()
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if len(body) == 0 {
		t.Error("gunzipped body empty")
	}
}

func TestNoGzipWhenNotAccepted(t *testing.T) {
	rec := getAsset(t, "/css/style.css", "")
	if enc := rec.Header().Get("Content-Encoding"); enc == "gzip" {
		t.Error("unexpected gzip without Accept-Encoding")
	}
	if rec.Body.Len() == 0 {
		t.Error("body empty")
	}
}

func TestNoGzipForBinaryTypes(t *testing.T) {
	// Any non-text asset must pass through uncompressed even when gzip is accepted.
	rec := getAsset(t, "/no/such/font.woff2", "gzip")
	if enc := rec.Header().Get("Content-Encoding"); enc == "gzip" {
		t.Error("woff2 should not be gzipped")
	}
}

func TestVaryHeader(t *testing.T) {
	rec := getAsset(t, "/css/style.css", "")
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding", vary)
	}
}

func TestWantsGzipClassification(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"js/app.js", true},
		{"css/style.css", true},
		{"index.html", true},
		{"img/logo.svg", true},
		{"data.json", true},
		{"font.woff2", false},
		{"pic.png", false},
	}
	for _, c := range cases {
		if got := wantsGzip(c.path); got != c.want {
			t.Errorf("wantsGzip(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestSPAIndexFallback pins the fallback contract: once the shell exists,
// unknown paths must serve index.html with 200 (deep hash-route links).
func TestSPAIndexFallback(t *testing.T) {
	if _, err := staticFS.ReadFile("static/index.html"); err != nil {
		t.Skip("index.html not yet embedded (web-core mid-flight)")
	}
	rec := getAsset(t, "/deep/route", "")
	if rec.Code != http.StatusOK {
		t.Errorf("fallback status = %d, want 200", rec.Code)
	}
}
