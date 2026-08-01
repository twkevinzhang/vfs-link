package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

var testWebP = []byte{'R', 'I', 'F', 'F', 6, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P'}

func newThumbnailRequest(t *testing.T, paths []string) *http.Request {
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
	return request
}

func postThumbnail(t *testing.T, handler http.Handler, paths []string) thumbnailResponse {
	t.Helper()
	request := newThumbnailRequest(t, paths)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST thumbnail status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response thumbnailResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
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
	firstRecord, found, findErr := store.FindThumbnail(ctx, first.ID)
	if findErr != nil || !found || firstRecord.DeleteAfter == nil {
		t.Fatalf("old thumbnail must be retained for delayed GC: record=%#v found=%v err=%v", firstRecord, found, findErr)
	}
	oldReader, openErr := objects.NewReader(ctx, "_vfs-link-thumbnails/"+first.ID+".webp")
	if openErr != nil {
		t.Fatalf("old thumbnail object must remain during grace period: %v", openErr)
	}
	_, _ = io.Copy(io.Discard, oldReader)
	_ = oldReader.Close()
	var deleted deletedResponse
	requestJSON(t, handler, http.MethodDelete, "/api/thumbnails", map[string]any{"paths": []string{"photos.z01", "photos.zip"}}, http.StatusOK, &deleted)
	if deleted.Deleted != 0 {
		t.Fatalf("deleted=%d, want deferred deletion", deleted.Deleted)
	}
	listing = FilesResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/files?path=", nil, http.StatusOK, &listing)
	if listing.Entries[0].Thumbnail != nil || listing.Entries[1].Thumbnail != nil {
		t.Fatalf("detached thumbnails still visible: %#v", listing.Entries)
	}
	collector := store.(db.ThumbnailGarbageCollector)
	deletedCount, err := collector.CleanupExpiredThumbnails(ctx, time.Now().UTC().Add(8*24*time.Hour), func(deleteCtx context.Context, orphan db.ThumbnailRecord) error {
		return objects.Delete(deleteCtx, orphan.PhysicalHash)
	})
	if err != nil || deletedCount != 2 {
		t.Fatalf("cleanup deleted=%d err=%v", deletedCount, err)
	}
	if _, found, findErr = store.FindThumbnail(ctx, second.ID); findErr != nil || found {
		t.Fatalf("expired thumbnail found=%v err=%v", found, findErr)
	}
}

type thumbnailFailureStore struct {
	db.Store
	publish bool
}

func (s thumbnailFailureStore) ReplaceThumbnail(ctx context.Context, record db.ThumbnailRecord, fileIDs []int) ([]db.ThumbnailRecord, error) {
	if s.publish {
		if _, err := s.Store.ReplaceThumbnail(ctx, record, fileIDs); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("injected thumbnail store failure")
}

func TestThumbnailStoreFailurePreservesPublishedObjectOnly(t *testing.T) {
	for _, test := range []struct {
		name       string
		publish    bool
		wantObject bool
	}{
		{name: "before publication", publish: false, wantObject: false},
		{name: "after publication", publish: true, wantObject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			if err = store.UpsertFile(ctx, "archive.zip", "archive-object", 1); err != nil {
				t.Fatal(err)
			}
			objects, err := blob.NewLocal(filepath.Join(root, "objects"))
			if err != nil {
				t.Fatal(err)
			}
			handler := New(thumbnailFailureStore{Store: store, publish: test.publish}, objects, nil, "", "").Handler()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, newThumbnailRequest(t, []string{"archive.zip"}))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			items, err := filepath.Glob(filepath.Join(root, "objects", "_vfs-link-thumbnails", "*.webp"))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(items) == 1; got != test.wantObject {
				t.Fatalf("object retained=%v, want %v; items=%#v", got, test.wantObject, items)
			}
			if test.wantObject {
				file, found, findErr := store.Find(ctx, "archive.zip")
				if findErr != nil || !found {
					t.Fatalf("archive found=%v err=%v", found, findErr)
				}
				linked, findErr := store.FindThumbnailsForFiles(ctx, []int{file.ID})
				if findErr != nil || linked[file.ID].ID == "" {
					t.Fatalf("published thumbnail=%#v err=%v", linked, findErr)
				}
			}
		})
	}
}
