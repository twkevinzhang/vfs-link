package blob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestInitiateResumableUploadReturnsLocationAndMetadataHeaders(t *testing.T) {
	var gotObject string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.URL.Query().Get("uploadType"); got != "resumable" {
			t.Errorf("uploadType = %q", got)
		}
		gotObject = r.URL.Query().Get("name")
		if got := r.Header.Get("X-Upload-Content-Type"); got != "video/mp4" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get("X-Upload-Content-Length"); got != "53687091200" {
			t.Errorf("content length = %q", got)
		}
		w.Header().Set("Location", serverURL(r)+"/session/opaque-token")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	location, err := initiateResumableUpload(context.Background(), server.Client(), server.URL, "bucket", "folder/My video.mp4", "video/mp4", 50*1024*1024*1024)
	if err != nil {
		t.Fatalf("initiateResumableUpload() error = %v", err)
	}
	if gotObject != "folder/My video.mp4" {
		t.Errorf("object = %q", gotObject)
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Path != "/session/opaque-token" {
		t.Errorf("location = %q", location)
	}
}

func TestInitiateResumableUploadRequiresLocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if _, err := initiateResumableUpload(context.Background(), server.Client(), server.URL, "bucket", "file", "", 1); err == nil {
		t.Fatal("initiateResumableUpload() error = nil, want missing Location error")
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
