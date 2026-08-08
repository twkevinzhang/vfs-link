package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSwaggerUIRedirectPreservesProxyPrefix(t *testing.T) {
	handler := (&Server{}).Handler()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(method, "/swagger", nil)
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusPermanentRedirect {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusPermanentRedirect)
			}
			if got := response.Header().Get("Location"); got != "swagger/" {
				t.Errorf("Location = %q, want swagger/", got)
			}
			if method == http.MethodHead && response.Body.Len() != 0 {
				t.Errorf("HEAD body length = %d, want 0", response.Body.Len())
			}
		})
	}
}

func TestSwaggerUIIndexUsesRelativeURLsAndEnablesAllMethods(t *testing.T) {
	response := serveSwaggerUIRequest(t, http.MethodGet, "/swagger/")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}

	body := response.Body.String()
	for _, relativeURL := range []string{
		`href="swagger-ui.css"`,
		`href="swagger-ui-overrides.css"`,
		`src="swagger-ui-bundle.js"`,
		`url: '../openapi.json'`,
	} {
		if !strings.Contains(body, relativeURL) {
			t.Errorf("index does not contain relative URL %q", relativeURL)
		}
	}
	for _, forbidden := range []string{"https://", "http://", `href="/`, `src="/`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("index contains non-relative URL marker %q", forbidden)
		}
	}
	if !strings.Contains(body, "persistAuthorization: false") {
		t.Error("index does not disable authorization persistence")
	}

	supportedMethods := []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
	for _, method := range supportedMethods {
		if !strings.Contains(body, `'`+method+`'`) {
			t.Errorf("supportedSubmitMethods does not contain %q", method)
		}
	}
}

func TestSwaggerUIAssets(t *testing.T) {
	tests := []struct {
		path        string
		contentType string
		bodyMarker  string
	}{
		{path: "/swagger/swagger-ui.css", contentType: "text/css; charset=utf-8", bodyMarker: ".swagger-ui"},
		{path: "/swagger/swagger-ui-overrides.css", contentType: "text/css; charset=utf-8", bodyMarker: "overflow-x"},
		{path: "/swagger/swagger-ui-bundle.js", contentType: "text/javascript; charset=utf-8", bodyMarker: "SwaggerUIBundle"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			getResponse := serveSwaggerUIRequest(t, http.MethodGet, test.path)
			if getResponse.Code != http.StatusOK {
				t.Fatalf("GET status = %d, want %d", getResponse.Code, http.StatusOK)
			}
			if got := getResponse.Header().Get("Content-Type"); got != test.contentType {
				t.Errorf("GET Content-Type = %q, want %q", got, test.contentType)
			}
			if !strings.Contains(getResponse.Body.String(), test.bodyMarker) {
				t.Errorf("GET body does not contain %q", test.bodyMarker)
			}
			if got := getResponse.Header().Get("Content-Length"); got != strconv.Itoa(getResponse.Body.Len()) {
				t.Errorf("GET Content-Length = %q, want %d", got, getResponse.Body.Len())
			}

			headResponse := serveSwaggerUIRequest(t, http.MethodHead, test.path)
			if headResponse.Code != http.StatusOK {
				t.Fatalf("HEAD status = %d, want %d", headResponse.Code, http.StatusOK)
			}
			if headResponse.Body.Len() != 0 {
				t.Errorf("HEAD body length = %d, want 0", headResponse.Body.Len())
			}
			if got := headResponse.Header().Get("Content-Length"); got != strconv.Itoa(getResponse.Body.Len()) {
				t.Errorf("HEAD Content-Length = %q, want %d", got, getResponse.Body.Len())
			}
		})
	}
}

func TestSwaggerUIRejectsUnsupportedMethods(t *testing.T) {
	for _, path := range []string{"/swagger", "/swagger/", "/swagger/swagger-ui.css"} {
		t.Run(path, func(t *testing.T) {
			response := serveSwaggerUIRequest(t, http.MethodPost, path)
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
			}
			if got := response.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want GET, HEAD", got)
			}
		})
	}
}

func TestSwaggerUIUnknownAssetReturnsNotFound(t *testing.T) {
	response := serveSwaggerUIRequest(t, http.MethodGet, "/swagger/unknown.js")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func serveSwaggerUIRequest(t *testing.T, method string, path string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	(&Server{}).Handler().ServeHTTP(response, request)
	return response
}
