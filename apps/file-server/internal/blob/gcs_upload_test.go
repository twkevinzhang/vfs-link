package blob

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
		if got := r.URL.Query().Get("ifGenerationMatch"); got != "0" {
			t.Errorf("ifGenerationMatch = %q, want 0", got)
		}
		gotObject = r.URL.Query().Get("name")
		if got := r.Header.Get("X-Upload-Content-Type"); got != "video/mp4" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get("X-Upload-Content-Length"); got != "53687091200" {
			t.Errorf("content length = %q", got)
		}
		if got := r.Header.Get("Origin"); got != "https://files.example.com" {
			t.Errorf("origin = %q", got)
		}
		w.Header().Set("Location", serverURL(r)+"/session/opaque-token")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	location, err := initiateResumableUpload(context.Background(), server.Client(), server.URL, "bucket", "folder/My video.mp4", "video/mp4", "https://files.example.com", 50*1024*1024*1024, 0)
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

	if _, err := initiateResumableUpload(context.Background(), server.Client(), server.URL, "bucket", "file", "", "", 1, 0); err == nil {
		t.Fatal("initiateResumableUpload() error = nil, want missing Location error")
	}
}

func TestInitiateResumableUploadOverwriteUsesGenerationPrecondition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("ifGenerationMatch"); got != "42" {
			t.Errorf("ifGenerationMatch = %q, want 42", got)
		}
		w.Header().Set("Location", serverURL(r)+"/session/overwrite")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if _, err := initiateResumableUpload(context.Background(), server.Client(), server.URL, "bucket", "file", "", "", 1, 42); err != nil {
		t.Fatal(err)
	}
}

func TestResumableOverwriteRaceReturnsPreconditionFailed(t *testing.T) {
	currentGeneration := int64(42)
	var requiredGeneration int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			requiredGeneration, _ = strconv.ParseInt(r.URL.Query().Get("ifGenerationMatch"), 10, 64)
			w.Header().Set("Location", serverURL(r)+"/session/racy-overwrite")
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			if requiredGeneration != currentGeneration {
				http.Error(w, "generation changed", http.StatusPreconditionFailed)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	location, err := initiateResumableUpload(context.Background(), server.Client(), server.URL, "bucket", "file", "", "", 4, currentGeneration)
	if err != nil {
		t.Fatal(err)
	}
	currentGeneration++ // another writer wins after session initiation
	request, err := http.NewRequest(http.MethodPut, location, strings.NewReader("data"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("upload status = %d, want 412", response.StatusCode)
	}
}

func TestCancelResumableUploadDeletesSessionURLOnly(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(499)
	}))
	defer server.Close()

	if err := cancelResumableUpload(context.Background(), server.Client(), server.URL+"/session/opaque"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/session/opaque" {
		t.Fatalf("cancel request = %s %s", gotMethod, gotPath)
	}
}

func TestResumableUploadHeaders(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		size        int64
		wantRange   string
	}{
		{name: "regular file", contentType: "video/mp4", size: 56_600_000, wantRange: "bytes 0-56599999/56600000"},
		{name: "empty file", size: 0, wantRange: "bytes */0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := resumableUploadHeaders(tt.contentType, tt.size)
			if got := headers["Content-Range"]; got != tt.wantRange {
				t.Fatalf("Content-Range = %q, want %q", got, tt.wantRange)
			}
			if tt.contentType != "" && headers["Content-Type"] != tt.contentType {
				t.Fatalf("Content-Type = %q, want %q", headers["Content-Type"], tt.contentType)
			}
		})
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
