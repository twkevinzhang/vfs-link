package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/share"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/upload"
)

type jsonRequestGCSStore struct {
	*blob.LocalStore
}

func (*jsonRequestGCSStore) Driver() string { return blob.DriverGCS }

func TestRequestBodyJSONPolicy(t *testing.T) {
	handler := newJSONRequestTestHandler(t)
	tests := []struct {
		name       string
		target     string
		validBody  string
		wantStatus int
	}{
		{
			name:       "file operation",
			target:     "/api/files/move",
			validBody:  `{"paths":["request-json/source.txt"],"destination":"request-json/destination"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "upload",
			target:     "/api/uploads",
			validBody:  `{"path":"request-json/upload.txt","size":3,"contentType":"text/plain","overwrite":false}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "share",
			target:     "/api/shares/drafts",
			validBody:  `{"path":"request-json/share.txt"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "drift",
			target:     "/api/drift/plans",
			validBody:  `{"paths":["request-json/drift.txt"]}`,
			wantStatus: http.StatusCreated,
		},
	}
	invalidBodies := []struct {
		name string
		body func(string) string
	}{
		{name: "empty", body: func(string) string { return "" }},
		{name: "malformed", body: func(string) string { return `{"broken":` }},
		{name: "unknown field", body: addUnknownJSONField},
		{name: "trailing JSON", body: func(valid string) string { return valid + ` {}` }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, invalid := range invalidBodies {
				t.Run(invalid.name, func(t *testing.T) {
					response := serveJSONRequest(handler, test.target, invalid.body(test.validBody))
					if response.Code != http.StatusBadRequest {
						t.Fatalf("status = %d body=%s, want 400", response.Code, response.Body.String())
					}
					var apiError map[string]string
					if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
						t.Fatalf("decode error response: %v", err)
					}
					if apiError["error"] != "invalid JSON body" || len(apiError) != 1 {
						t.Fatalf("error response = %#v", apiError)
					}
				})
			}

			t.Run("valid", func(t *testing.T) {
				response := serveJSONRequest(handler, test.target, test.validBody)
				if response.Code != test.wantStatus {
					t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
				}
			})
		})
	}
}

func newJSONRequestTestHandler(t *testing.T) http.Handler {
	t.Helper()
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
	localObjects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	objects := &jsonRequestGCSStore{LocalStore: localObjects}
	thumbnailObjects, err := blob.NewLocal(filepath.Join(root, "thumbnails"))
	if err != nil {
		t.Fatal(err)
	}

	for _, directory := range []string{"request-json", "request-json/destination"} {
		if err := metadata.UpsertDirectory(ctx, directory); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []struct {
		path string
		hash string
		size int64
	}{
		{path: "request-json/source.txt", hash: "source-object", size: 1},
		{path: "request-json/share.txt", hash: "share-object", size: 2},
		{path: "request-json/drift.txt", hash: "legacy-object", size: 3},
	} {
		if err := metadata.UpsertFile(ctx, record.path, record.hash, record.size); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := objects.NewWriter(ctx, "legacy-object")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("old")); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	shareService := share.NewService(config.Config{
		ShareGCSBucket: "contract-share-bucket",
		ShareGCSPrefix: "shares",
		SharePublicURL: "https://example.test",
	}, metadata, objects, logger)
	uploadService := upload.NewWithBlob(metadata, objects)
	handler := New(metadata, objects, thumbnailObjects, shareService, "", "", uploadService).
		SetDriftEnabled(true).
		Handler()

	refresh := httptest.NewRecorder()
	handler.ServeHTTP(refresh, httptest.NewRequest(http.MethodGet, "/api/drift?refresh=true", nil))
	if refresh.Code != http.StatusOK {
		t.Fatalf("prepare drift snapshot = %d %s", refresh.Code, refresh.Body.String())
	}
	return handler
}

func addUnknownJSONField(valid string) string {
	var value map[string]any
	if err := json.Unmarshal([]byte(valid), &value); err != nil {
		panic(err)
	}
	value["unexpected"] = true
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func serveJSONRequest(handler http.Handler, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
