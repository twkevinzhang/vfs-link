package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/httpauth"
)

func TestSwaggerUIUsesPublicHTTPBasicAuth(t *testing.T) {
	handler := httpauth.Basic(true, "operator", "secret", (&Server{}).Handler())

	for _, requestPath := range []string{
		"/swagger/",
		"/swagger/swagger-ui.css",
		"/swagger/swagger-ui-overrides.css",
		"/swagger/swagger-ui-bundle.js",
		"/openapi.json",
	} {
		t.Run(requestPath, func(t *testing.T) {
			unauthorized := httptest.NewRecorder()
			handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, requestPath, nil))
			if unauthorized.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
			}
			if unauthorized.Header().Get("WWW-Authenticate") == "" {
				t.Error("unauthorized response has no WWW-Authenticate challenge")
			}

			authorized := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, requestPath, nil)
			request.SetBasicAuth("operator", "secret")
			handler.ServeHTTP(authorized, request)
			if authorized.Code != http.StatusOK {
				t.Fatalf("authorized status = %d, want %d", authorized.Code, http.StatusOK)
			}
		})
	}
}
