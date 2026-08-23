// Package server — /api/providers and /api/models handlers for the
// redesigned attribution views (vendor rollups, model details with trends).
package server

import (
	"net/http"

	"xusage/internal/engine"
)

func handleProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, engine.Get().ByVendor(optsFromRequest(r)))
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	trend := r.URL.Query().Get("trend") == "1"
	writeJSON(w, http.StatusOK, engine.Get().ModelsDetailed(optsFromRequest(r), trend))
}
