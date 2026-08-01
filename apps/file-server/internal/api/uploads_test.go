package api

import (
	"net/http/httptest"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/upload"
)

func TestRequestOriginPrefersBrowserOrigin(t *testing.T) {
	request := httptest.NewRequest("POST", "http://internal/api/uploads", nil)
	request.Header.Set("Origin", "https://files.example.com")
	request.Header.Set("X-Forwarded-Proto", "https")

	if got := requestOrigin(request); got != "https://files.example.com" {
		t.Fatalf("requestOrigin() = %q", got)
	}
}

func TestUploadStatusDoesNotExposePersistedGCSCapability(t *testing.T) {
	session := upload.Session{
		ID:            "upload-id",
		Driver:        "gcs",
		UploadURL:     "https://storage.example/opaque-session",
		UploadHeaders: map[string]string{"Content-Type": "text/plain"},
	}
	created := toUploadResponse(session, true)
	if created.UploadURL == "" || len(created.Headers) == 0 {
		t.Fatal("create response must include the resumable upload capability")
	}
	status := toUploadResponse(session, false)
	if status.UploadURL != "" || len(status.Headers) != 0 {
		t.Fatalf("status exposed upload capability: url=%q headers=%v", status.UploadURL, status.Headers)
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
