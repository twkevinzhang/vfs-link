package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
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

func TestUploadStatusRestoresPersistedGCSCapability(t *testing.T) {
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
		t.Fatalf("explicitly hidden capability: url=%q headers=%v", status.UploadURL, status.Headers)
	}
	status = toUploadResponse(session, true)
	if status.UploadURL != session.UploadURL || status.Headers["Content-Type"] != "text/plain" {
		t.Fatalf("restored capability: url=%q headers=%v", status.UploadURL, status.Headers)
	}
}

func TestLocalChunkUploadResumesFromCommittedOffset(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	metadata, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := upload.NewWithBlob(metadata, objects)
	handler := New(metadata, objects, objects, nil, "", "", service).Handler()

	createBody := bytes.NewBufferString(`{"path":"docs/resume.txt","size":6,"contentType":"text/plain","overwrite":false}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/uploads", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", createResponse.Code, createResponse.Body.String())
	}
	var session uploadResponse
	if err := json.Unmarshal(createResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	putChunk := func(contentRange, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, session.UploadURL, bytes.NewBufferString(body))
		request.Header.Set("Content-Range", contentRange)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	partial := putChunk("bytes 0-2/6", "abc")
	if partial.Code != 308 || partial.Header().Get("Range") != "bytes=0-2" {
		t.Fatalf("partial = %d range=%q body=%s", partial.Code, partial.Header().Get("Range"), partial.Body.String())
	}
	var partialSession uploadResponse
	if err := json.Unmarshal(partial.Body.Bytes(), &partialSession); err != nil || partialSession.UploadedSize != 3 {
		t.Fatalf("partial session = %#v, %v", partialSession, err)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, session.StatusURL, nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	var resumed uploadResponse
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &resumed); err != nil || resumed.UploadedSize != 3 || resumed.UploadURL == "" {
		t.Fatalf("resume status = %#v, %v", resumed, err)
	}

	conflict := putChunk("bytes 0-2/6", "abc")
	if conflict.Code != http.StatusConflict || conflict.Header().Get("Range") != "bytes=0-2" {
		t.Fatalf("offset conflict = %d range=%q body=%s", conflict.Code, conflict.Header().Get("Range"), conflict.Body.String())
	}
	finalChunk := putChunk("bytes 3-5/6", "def")
	if finalChunk.Code != http.StatusOK || finalChunk.Header().Get("Range") != "bytes=0-5" {
		t.Fatalf("final chunk = %d range=%q body=%s", finalChunk.Code, finalChunk.Header().Get("Range"), finalChunk.Body.String())
	}

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, session.CompleteURL, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("complete attempt %d = %d %s", attempt+1, response.Code, response.Body.String())
		}
	}
}

func TestParseUploadContentRange(t *testing.T) {
	start, end, total, err := parseUploadContentRange("bytes 8-15/32", 8)
	if err != nil || start != 8 || end != 15 || total != 32 {
		t.Fatalf("parsed = %d-%d/%d, %v", start, end, total, err)
	}
	for _, value := range []string{"items 0-1/2", "bytes 2-1/3", "bytes 0-3/3", "bytes 0-x/3"} {
		if _, _, _, err := parseUploadContentRange(value, -1); err == nil {
			t.Fatalf("parseUploadContentRange(%q) error = nil", value)
		}
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
