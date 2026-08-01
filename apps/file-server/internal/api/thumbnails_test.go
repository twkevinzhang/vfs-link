package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

var testWebP = []byte{'R', 'I', 'F', 'F', 6, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P'}

func postThumbnail(t *testing.T, handler http.Handler, paths []string) thumbnailResponse {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	pathsJSON, _ := json.Marshal(paths)
	_ = writer.WriteField("paths", string(pathsJSON))
	_ = writer.WriteField("width", "32")
	_ = writer.WriteField("height", "24")
	part, err := writer.CreateFormFile("thumbnail", "thumbnail.webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(testWebP); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/thumbnails", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST thumbnail status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response thumbnailResponse
	if err = json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestThumbnailCreateListGetAndReplace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"photos.z01", "photos.zip"} {
		if err = store.UpsertFile(ctx, path, "object-"+path, 10); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, objects, nil, "", "").Handler()
	first := postThumbnail(t, handler, []string{"photos.z01", "photos.zip"})
	if first.ID == "" || first.Width != 32 || first.Height != 24 {
		t.Fatalf("response=%#v", first)
	}
	var listing FilesResponse
	requestJSON(t, handler, http.MethodGet, "/api/files?path=", nil, http.StatusOK, &listing)
	if len(listing.Entries) != 2 || listing.Entries[0].Thumbnail == nil || listing.Entries[1].Thumbnail == nil || listing.Entries[0].Thumbnail.ID != first.ID || listing.Entries[1].Thumbnail.ID != first.ID {
		t.Fatalf("listing thumbnails=%#v", listing.Entries)
	}
	request := httptest.NewRequest(http.MethodGet, first.URL, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), testWebP) || recorder.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("GET thumbnail status=%d type=%q body=%v", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.Bytes())
	}
	second := postThumbnail(t, handler, []string{"photos.z01", "photos.zip"})
	if second.ID == first.ID {
		t.Fatal("replacement reused thumbnail id")
	}
	if _, found, findErr := store.FindThumbnail(ctx, first.ID); findErr != nil || found {
		t.Fatalf("old thumbnail metadata found=%v err=%v", found, findErr)
	}
	oldReader, openErr := objects.NewReader(ctx, "_vfs-link-thumbnails/"+first.ID+".webp")
	if openErr == nil {
		_, _ = io.Copy(io.Discard, oldReader)
		_ = oldReader.Close()
		t.Fatal("old thumbnail object still exists")
	}
	var deleted deletedResponse
	requestJSON(t, handler, http.MethodDelete, "/api/thumbnails", map[string]any{"paths": []string{"photos.z01", "photos.zip"}}, http.StatusOK, &deleted)
	if deleted.Deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted.Deleted)
	}
	if _, found, findErr := store.FindThumbnail(ctx, second.ID); findErr != nil || found {
		t.Fatalf("cleared thumbnail found=%v err=%v", found, findErr)
	}
}
