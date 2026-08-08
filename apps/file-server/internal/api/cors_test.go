package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithCORSRestrictsOrigins(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := withCORS(next, map[string]struct{}{"https://files.example": {}})

	request := httptest.NewRequest(http.MethodOptions, "http://api.example/api/files", nil)
	request.Header.Set("Origin", "https://files.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "https://files.example" {
		t.Fatalf("allowed origin response = %d, %q", response.Code, response.Header().Get("Access-Control-Allow-Origin"))
	}
	if got := response.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Content-Range") {
		t.Fatalf("allowed headers = %q", got)
	}
	if got := response.Header().Get("Access-Control-Expose-Headers"); got != "Range" {
		t.Fatalf("exposed headers = %q", got)
	}

	request = httptest.NewRequest(http.MethodGet, "http://api.example/api/files", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin header = %q", got)
	}
}
