package api

import (
	_ "embed"
	"net/http"
)

// openAPIDocument is embedded so /openapi.json remains available in the
// production image, which only needs the file-server binary at runtime.
//
//go:embed openapi.json
var openAPIDocument []byte

func handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(openAPIDocument)
}
