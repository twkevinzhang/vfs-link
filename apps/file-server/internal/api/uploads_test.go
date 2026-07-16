package api

import (
	"net/http/httptest"
	"testing"
)

func TestRequestOriginPrefersBrowserOrigin(t *testing.T) {
	request := httptest.NewRequest("POST", "http://internal/api/uploads", nil)
	request.Header.Set("Origin", "https://files.example.com")
	request.Header.Set("X-Forwarded-Proto", "https")

	if got := requestOrigin(request); got != "https://files.example.com" {
		t.Fatalf("requestOrigin() = %q", got)
	}
}

func TestRequestOriginUsesForwardedCloudRunURL(t *testing.T) {
	request := httptest.NewRequest("POST", "http://vfs-link-file-server/api/uploads", nil)
	request.Host = "vfs-link-file-server.example.run.app"
	request.Header.Set("X-Forwarded-Proto", "https")

	if got := requestOrigin(request); got != "https://vfs-link-file-server.example.run.app" {
		t.Fatalf("requestOrigin() = %q", got)
	}
}
