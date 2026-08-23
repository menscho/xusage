package server

import (
	"net/http"

	"xusage/internal/web"
)

// NewHandler returns the full production handler — every API route plus the
// embedded dashboard — without binding a socket. Run composes the same mux
// with request logging on top; tests use this to exercise the real serving
// stack via httptest.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	registerRoutes(mux)
	mux.Handle("/", web.Handler())
	return mux
}
