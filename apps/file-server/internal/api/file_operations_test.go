package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func TestFileOperationsAPITrashLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewJSONLocal(filepath.Join(root, "metadata.json"))
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

	for _, directory := range []string{"/source", "/target"} {
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
	if err := store.UpsertFile(ctx, "/source/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}

	handler := New(store, objects, nil, "", "").Handler()
	requestJSON(t, handler, http.MethodPost, "/api/files/move", map[string]any{
		"paths": []string{"/source/a.txt"}, "destination": "/target",
	}, http.StatusOK, nil)
	if _, found, err := store.Find(ctx, "/target/a.txt"); err != nil || !found {
		t.Fatalf("moved file found=%v err=%v", found, err)
	}

	var trashed entriesResponse
	requestJSON(t, handler, http.MethodPost, "/api/files/trash", map[string]any{
		"paths": []string{"/target/a.txt"},
	}, http.StatusOK, &trashed)
	if len(trashed.Entries) != 1 || trashed.Entries[0].TrashID == "" {
		t.Fatalf("trashed entries = %#v", trashed.Entries)
	}
	trashID := trashed.Entries[0].TrashID
	if _, found, err := store.Find(ctx, "/target/a.txt"); err != nil || found {
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
	if _, found, err := store.Find(ctx, "/target/a.txt"); err != nil || !found {
		t.Fatalf("restored file found=%v err=%v", found, err)
	}

	trashed = entriesResponse{}
	requestJSON(t, handler, http.MethodPost, "/api/files/trash", map[string]any{
		"paths": []string{"/target/a.txt"},
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
