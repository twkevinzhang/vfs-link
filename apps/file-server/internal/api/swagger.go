package api

import (
	"embed"
	"net/http"
	"strconv"
)

// swaggerUIFiles contains the minimum runtime assets from swagger-ui-dist
// 5.32.12 (npm integrity
// sha512-ceXE+KNc5LXafhh36trB6nxK+U2EIKUWzHT7sBiMosf2eJLAefalYnTwMouQBmSyED6m7Yz+GYZEcL+Glt3sww==).
// Keeping the distribution embedded makes the documentation UI available
// without a CDN or any other runtime dependency.
//
//go:embed swaggerui/*
var swaggerUIFiles embed.FS

type swaggerUIRoute struct {
	fileName    string
	contentType string
}

var swaggerUIRoutes = map[string]swaggerUIRoute{
	"/swagger/":                         {fileName: "index.html", contentType: "text/html; charset=utf-8"},
	"/swagger/swagger-ui.css":           {fileName: "swagger-ui.css", contentType: "text/css; charset=utf-8"},
	"/swagger/swagger-ui-overrides.css": {fileName: "swagger-ui-overrides.css", contentType: "text/css; charset=utf-8"},
	"/swagger/swagger-ui-bundle.js":     {fileName: "swagger-ui-bundle.js", contentType: "text/javascript; charset=utf-8"},
}

func handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if r.URL.Path == "/swagger" {
		w.Header().Set("Location", "swagger/")
		w.WriteHeader(http.StatusPermanentRedirect)
		return
	}

	route, ok := swaggerUIRoutes[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}

	contents, err := swaggerUIFiles.ReadFile("swaggerui/" + route.fileName)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", route.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(contents)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(contents)
}
