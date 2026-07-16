package httpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasic(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := Basic(true, "operator", "secret", next)

	request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthenticated response = %d, challenge %q", response.Code, response.Header().Get("WWW-Authenticate"))
	}

	request = httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	request.SetBasicAuth("operator", "secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authenticated response = %d", response.Code)
	}
}

func TestBasicDisabled(t *testing.T) {
	handler := Basic(false, "", "", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("response = %d", response.Code)
	}
}
