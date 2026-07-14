package webdav

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := basicAuth("dav-user", "dav-pass", next)

	request := httptest.NewRequest(http.MethodGet, "https://example.com/dav/file.txt", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodGet, "https://example.com/dav/file.txt", nil)
	request.SetBasicAuth("dav-user", "dav-pass")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestSecureRequestsRequiresHTTPS(t *testing.T) {
	handler := secureRequests("/dav/", true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://example.com/dav/file.txt", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUpgradeRequired)
	}

	request.Header.Set("X-Forwarded-Proto", "https")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("forwarded HTTPS status = %d, want %d", response.Code, http.StatusNoContent)
	}

	handler = secureRequests("/dav/", false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUpgradeRequired {
		t.Fatalf("untrusted forwarded HTTPS status = %d, want %d", response.Code, http.StatusUpgradeRequired)
	}
}

func TestHTTPSGateRunsBeforeBasicAuth(t *testing.T) {
	handler := secureRequests("/dav/", false, basicAuth("dav-user", "dav-pass", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	request := httptest.NewRequest(http.MethodGet, "http://example.com/dav/file.txt", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUpgradeRequired)
	}
	if challenge := response.Header().Get("WWW-Authenticate"); challenge != "" {
		t.Fatalf("plaintext response exposed Basic challenge %q", challenge)
	}
}

func TestSecureRequestsRejectsUnsafeDepthAndDestination(t *testing.T) {
	handler := secureRequests("/dav/", false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest("PROPFIND", "https://example.com/dav/", nil)
	request.Header.Set("Depth", "infinity")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("infinite depth status = %d, want %d", response.Code, http.StatusForbidden)
	}

	request = httptest.NewRequest("MOVE", "https://example.com/dav/source", nil)
	request.Header.Set("Destination", "https://other.example/dav/target")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("cross-origin destination status = %d, want %d", response.Code, http.StatusBadGateway)
	}

	request = httptest.NewRequest("MOVE", "https://example.com/dav/source", nil)
	request.Header.Set("Destination", "https://example.com/dav/source/child")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("descendant destination status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestPathWithinPrefix(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{"/dav", true},
		{"/dav/folder/file.txt", true},
		{"/dav-other/file.txt", false},
		{"/api/status", false},
	} {
		if got := pathWithinPrefix(test.path, "/dav/"); got != test.want {
			t.Errorf("pathWithinPrefix(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}
