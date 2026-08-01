package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type blockingOperationStore struct {
	db.Store
	db.TreeOperationStore
	runs    atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingOperationStore) RunOperation(ctx context.Context, id string) (db.OperationRecord, error) {
	s.runs.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return db.OperationRecord{}, ctx.Err()
	}
	return s.TreeOperationStore.RunOperation(ctx, id)
}

func TestFileOperationsAPITrashLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}

	for _, directory := range []string{"source", "target"} {
		if err := store.UpsertDirectory(ctx, directory); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := objects.NewWriter(ctx, "object-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "source/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}

	handler := New(store, objects, objects, nil, "", "").Handler()
	requestJSON(t, handler, http.MethodPost, "/api/files/move", map[string]any{
		"paths": []string{"source/a.txt"}, "destination": "target",
	}, http.StatusOK, nil)
	if _, found, err := store.Find(ctx, "target/a.txt"); err != nil || !found {
		t.Fatalf("moved file found=%v err=%v", found, err)
	}

	var trashed entriesResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/trash", map[string]any{
		"paths": []string{"target/a.txt"},
	}, http.StatusOK, &trashed)
	if len(trashed.Entries) != 1 || trashed.Entries[0].TrashID == "" {
		t.Fatalf("trashed entries = %#v", trashed.Entries)
	}
	trashID := trashed.Entries[0].TrashID
	if _, found, err := store.Find(ctx, "target/a.txt"); err != nil || found {
		t.Fatalf("trashed file leaked into active namespace: found=%v err=%v", found, err)
	}

	var trash entriesResponse
	requestJSON(t, handler, http.MethodGet, "/api/trash", nil, http.StatusOK, &trash)
	if len(trash.Entries) != 1 || trash.Entries[0].TrashID != trashID {
		t.Fatalf("trash entries = %#v", trash.Entries)
	}
	requestJSON(t, handler, http.MethodPost, "/api/trash/restore", map[string]any{
		"trashIds": []string{trashID},
	}, http.StatusOK, nil)
	if _, found, err := store.Find(ctx, "target/a.txt"); err != nil || !found {
		t.Fatalf("restored file found=%v err=%v", found, err)
	}

	trashed = entriesResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/files/trash", map[string]any{
		"paths": []string{"target/a.txt"},
	}, http.StatusOK, &trashed)
	requestJSON(t, handler, http.MethodPost, "/api/trash/delete", map[string]any{
		"trashIds": []string{trashed.Entries[0].TrashID},
	}, http.StatusOK, nil)
	if _, err := os.Stat(filepath.Join(objects.Root(), "object-a")); !os.IsNotExist(err) {
		t.Fatalf("permanently deleted object stat error = %v", err)
	}
	if records, err := store.ListTrashRecords(ctx, nil); err != nil || len(records) != 0 {
		t.Fatalf("remaining trash records=%d err=%v", len(records), err)
	}
}

func TestTreeDirectoryMoveReturnsAcceptedOperationAndCanBePolled(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"source", "target"} {
		if err := store.UpsertDirectory(ctx, directory); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertFile(ctx, "source/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}

	handler := New(store, objects, objects, nil, "", "").Handler()
	var accepted operationResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/move", map[string]any{
		"paths": []string{"source"}, "destination": "target",
	}, http.StatusAccepted, &accepted)
	if accepted.OperationID == "" || accepted.Status != "pending" {
		t.Fatalf("accepted operation = %#v", accepted)
	}

	deadline := time.Now().Add(5 * time.Second)
	var completed operationResponse
	for {
		var current operationResponse
		requestJSON(t, handler, http.MethodGet, "/api/operations/"+accepted.OperationID, nil, http.StatusOK, &current)
		if current.Status == "completed" {
			if len(current.Entries) == 0 {
				t.Fatalf("completed operation has no entries: %#v", current)
			}
			completed = current
			break
		}
		if current.Status == "failed" {
			t.Fatalf("operation failed: %#v", current)
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation did not complete: %#v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, found, err := store.Find(ctx, "target/source/a.txt"); err != nil || !found {
		t.Fatalf("moved descendant found=%v err=%v operation=%#v", found, err, completed)
	}
}

func TestTreeFileMoveRemainsSynchronous(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}

	var moved entriesResponse
	requestJSON(t, New(store, objects, objects, nil, "", "").Handler(), http.MethodPost, "/api/files/move", map[string]any{
		"paths": []string{"a.txt"}, "destination": "target",
	}, http.StatusOK, &moved)
	if len(moved.Entries) != 1 || moved.Entries[0].Path != "target/a.txt" {
		t.Fatalf("moved entries = %#v", moved.Entries)
	}
}

func TestFileRenameAPIValidatesNamesConflictsAndTrimsUnicode(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "folder"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "folder/old.txt", "object-old", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "folder/taken.txt", "object-taken", 1); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, objects, objects, nil, "", "").Handler()

	var renamed entriesResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/rename", map[string]any{
		"path": "folder/old.txt", "name": "  \u6e2c\u8a66\u6a94\u6848.txt  ",
	}, http.StatusOK, &renamed)
	if len(renamed.Entries) != 1 || renamed.Entries[0].Path != "folder/\u6e2c\u8a66\u6a94\u6848.txt" {
		t.Fatalf("renamed entries = %#v", renamed.Entries)
	}
	if renamed.Entries[0].PhysicalHash != "object-old" {
		t.Fatalf("rename changed physical hash = %q", renamed.Entries[0].PhysicalHash)
	}
	if _, found, err := store.Find(ctx, "folder/old.txt"); err != nil || found {
		t.Fatalf("old entry remains found=%v err=%v", found, err)
	}

	requestJSON(t, handler, http.MethodPost, "/api/files/rename", map[string]any{
		"path": "folder/\u6e2c\u8a66\u6a94\u6848.txt", "name": "taken.txt",
	}, http.StatusConflict, nil)
	requestJSON(t, handler, http.MethodPost, "/api/files/rename", map[string]any{
		"path": "missing.txt", "name": "renamed.txt",
	}, http.StatusNotFound, nil)
	requestJSON(t, handler, http.MethodPost, "/api/files/rename", map[string]any{
		"path": "folder/\u6e2c\u8a66\u6a94\u6848.txt", "name": "\u6e2c\u8a66\u6a94\u6848.txt",
	}, http.StatusBadRequest, nil)
	for _, name := range []string{"", " ", ".", "..", "nested/name"} {
		requestJSON(t, handler, http.MethodPost, "/api/files/rename", map[string]any{
			"path": "folder/\u6e2c\u8a66\u6a94\u6848.txt", "name": name,
		}, http.StatusBadRequest, nil)
	}
	requestJSON(t, handler, http.MethodPost, "/api/files/rename", map[string]any{
		"path": "", "name": "not-root",
	}, http.StatusBadRequest, nil)
}

func TestTreeDirectoryRenameReturnsAcceptedOperationAndCanBePolled(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "source/nested"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "source/nested/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, objects, objects, nil, "", "").Handler()

	var accepted operationResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/rename", map[string]any{
		"path": "source", "name": "renamed",
	}, http.StatusAccepted, &accepted)
	if accepted.OperationID == "" || accepted.Type != "rename" || accepted.Status != "pending" {
		t.Fatalf("accepted rename operation = %#v", accepted)
	}
	completed := pollOperation(t, handler, accepted.OperationID)
	if completed.Type != "rename" || len(completed.Entries) != 1 || completed.Entries[0].Path != "renamed" {
		t.Fatalf("completed rename operation = %#v", completed)
	}
	if _, found, err := store.Find(ctx, "renamed/nested/a.txt"); err != nil || !found {
		t.Fatalf("renamed descendant found=%v err=%v", found, err)
	}
	descendant, found, err := store.Find(ctx, "renamed/nested/a.txt")
	if err != nil || !found || descendant.PhysicalHash != "object-a" {
		t.Fatalf("renamed descendant mapping=%+v found=%v err=%v", descendant, found, err)
	}
}

func TestTreeDirectoryTrashAndRestoreReturnAcceptedOperations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "folder"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "folder/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, objects, objects, nil, "", "").Handler()

	var accepted operationResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/trash", map[string]any{
		"paths": []string{"folder"},
	}, http.StatusAccepted, &accepted)
	completed := pollOperation(t, handler, accepted.OperationID)
	if len(completed.Entries) == 0 || completed.Entries[0].TrashID == "" {
		t.Fatalf("trash operation entries = %#v", completed.Entries)
	}

	accepted = operationResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/trash/restore", map[string]any{
		"trashIds": []string{completed.Entries[0].TrashID},
	}, http.StatusAccepted, &accepted)
	pollOperation(t, handler, accepted.OperationID)
	if _, found, err := store.Find(ctx, "folder/a.txt"); err != nil || !found {
		t.Fatalf("restored descendant found=%v err=%v", found, err)
	}
}

func TestTreeDirectoryPermanentDeleteRunsDurableOperation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "folder"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "folder/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := objects.NewWriter(ctx, "object-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	handler := New(store, objects, objects, nil, "", "").Handler()

	var accepted operationResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/trash", map[string]any{
		"paths": []string{"folder"},
	}, http.StatusAccepted, &accepted)
	trashed := pollOperation(t, handler, accepted.OperationID)
	trashID := trashed.Entries[0].TrashID

	accepted = operationResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/trash/delete", map[string]any{
		"trashIds": []string{trashID},
	}, http.StatusAccepted, &accepted)
	deleted := pollOperation(t, handler, accepted.OperationID)
	if deleted.Deleted == 0 || deleted.Progress != 1 || deleted.Total != 1 {
		t.Fatalf("delete operation = %#v", deleted)
	}
	if _, err := os.Stat(filepath.Join(objects.Root(), "object-a")); !os.IsNotExist(err) {
		t.Fatalf("permanently deleted object stat error = %v", err)
	}
	if trash, err := store.ListTrash(ctx); err != nil || len(trash) != 0 {
		t.Fatalf("remaining trash=%d err=%v", len(trash), err)
	}
}

func pollOperation(t *testing.T, handler http.Handler, id string) operationResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var current operationResponse
		requestJSON(t, handler, http.MethodGet, "/api/operations/"+id, nil, http.StatusOK, &current)
		switch current.Status {
		case "completed":
			return current
		case "failed":
			t.Fatalf("operation failed: %#v", current)
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation did not complete: %#v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOperationPollsDoNotStartDuplicateInProcessWorkers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base, err := db.NewTreeLocal(filepath.Join(root, "tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	if err := base.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := base.UpsertDirectory(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if err := base.UpsertDirectory(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	treeOperations := base.(db.TreeOperationStore)
	store := &blockingOperationStore{
		Store: base, TreeOperationStore: treeOperations,
		started: make(chan struct{}), release: make(chan struct{}),
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(store, objects, objects, nil, "", "")
	handler := server.Handler()
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(store.release) }) }
	t.Cleanup(func() {
		releaseWorker()
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.files.WaitOperations(waitCtx); err != nil {
			t.Errorf("wait operation workers during cleanup: %v", err)
		}
	})
	var accepted operationResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/move", map[string]any{
		"paths": []string{"source"}, "destination": "target",
	}, http.StatusAccepted, &accepted)
	<-store.started
	for range 5 {
		requestJSON(t, handler, http.MethodGet, "/api/operations/"+accepted.OperationID, nil, http.StatusOK, nil)
	}
	if got := store.runs.Load(); got != 1 {
		t.Fatalf("RunOperation calls = %d, want 1", got)
	}
	releaseWorker()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.files.WaitOperations(waitCtx); err != nil {
		t.Fatalf("wait operation workers: %v", err)
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, target string, body any, wantStatus int, response any) {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, target, &payload)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status=%d body=%s", method, target, recorder.Code, recorder.Body.String())
	}
	if response != nil {
		if err := json.NewDecoder(recorder.Body).Decode(response); err != nil {
			t.Fatal(err)
		}
	}
}
