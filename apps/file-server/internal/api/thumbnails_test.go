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
	"sync/atomic"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

var testWebP = []byte{'R', 'I', 'F', 'F', 6, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P'}

// countingBlobStore makes cross-bucket regressions observable: thumbnail
// endpoints must never perform a blob operation against the primary store.
type countingBlobStore struct {
	blob.Store
	newWriterCalls atomic.Int64
	newReaderCalls atomic.Int64
	deleteCalls    atomic.Int64
}

func (s *countingBlobStore) NewWriter(ctx context.Context, name string) (io.WriteCloser, error) {
	s.newWriterCalls.Add(1)
	return s.Store.NewWriter(ctx, name)
}

func (s *countingBlobStore) NewReader(ctx context.Context, name string) (io.ReadCloser, error) {
	s.newReaderCalls.Add(1)
	return s.Store.NewReader(ctx, name)
}

func (s *countingBlobStore) Delete(ctx context.Context, name string) error {
	s.deleteCalls.Add(1)
	return s.Store.Delete(ctx, name)
}

func (s *countingBlobStore) calls() int64 {
	return s.newWriterCalls.Load() + s.newReaderCalls.Load() + s.deleteCalls.Load()
}

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
	thumbnailObjects, err := blob.NewLocal(filepath.Join(root, "thumbnails"))
	if err != nil {
		t.Fatal(err)
	}
	primary := &countingBlobStore{Store: objects}
	thumbnails := &countingBlobStore{Store: thumbnailObjects}
	handler := New(store, primary, thumbnails, nil, "", "").Handler()
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
	oldReader, openErr := thumbnailObjects.NewReader(ctx, "_vfs-link-thumbnails/"+first.ID+".webp")
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
		return thumbnails.Delete(deleteCtx, orphan.PhysicalHash)
	})
	if err != nil || deletedCount != 2 {
		t.Fatalf("cleanup deleted=%d err=%v", deletedCount, err)
	}
	if _, found, findErr = store.FindThumbnail(ctx, second.ID); findErr != nil || found {
		t.Fatalf("expired thumbnail found=%v err=%v", found, findErr)
	}
	if got := primary.calls(); got != 0 {
		t.Fatalf("primary store received %d thumbnail blob operations", got)
	}
	if thumbnails.newWriterCalls.Load() != 2 || thumbnails.newReaderCalls.Load() == 0 || thumbnails.deleteCalls.Load() != 2 {
		t.Fatalf("thumbnail store calls writes=%d reads=%d deletes=%d", thumbnails.newWriterCalls.Load(), thumbnails.newReaderCalls.Load(), thumbnails.deleteCalls.Load())
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
			thumbnailObjects, err := blob.NewLocal(filepath.Join(root, "thumbnails"))
			if err != nil {
				t.Fatal(err)
			}
			primary := &countingBlobStore{Store: objects}
			thumbnails := &countingBlobStore{Store: thumbnailObjects}
			handler := New(thumbnailFailureStore{Store: store, publish: test.publish}, primary, thumbnails, nil, "", "").Handler()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, newThumbnailRequest(t, []string{"archive.zip"}))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			items, err := filepath.Glob(filepath.Join(root, "thumbnails", "_vfs-link-thumbnails", "*.webp"))
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
			if got := primary.calls(); got != 0 {
				t.Fatalf("primary store received %d thumbnail failure-path operations", got)
			}
			if thumbnails.newWriterCalls.Load() != 1 {
				t.Fatalf("thumbnail write calls=%d, want 1", thumbnails.newWriterCalls.Load())
			}
			if got, want := thumbnails.deleteCalls.Load(), map[bool]int64{false: 1, true: 0}[test.publish]; got != want {
				t.Fatalf("thumbnail delete calls=%d, want %d", got, want)
			}
		})
	}
}
