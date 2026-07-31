package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
)

func TestNewMetadataStoreJSONLocal(t *testing.T) {
	root := t.TempDir()
	store, err := newMetadataStore(context.Background(), config.Config{
		DatabaseDriver:        "json",
		MetadataStorageDriver: "local",
		MetadataLocalRoot:     root,
		MetadataPrefix:        "_vfs-link",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(context.Background(), "report.txt", "object.txt", 4); err != nil {
		t.Fatal(err)
	}
	if record, found, err := store.Find(context.Background(), "report.txt"); err != nil || !found || record.Size != 4 {
		t.Fatalf("Find() = %#v, %t, %v", record, found, err)
	}
	if _, err := os.Stat(filepath.Join(root, "_vfs-link", "stats.json")); err != nil {
		t.Fatalf("tree metadata stats: %v", err)
	}
}

func TestMaintenanceMode(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		recorder := httptest.NewRecorder()
		maintenanceMode(true, next).ServeHTTP(recorder, httptest.NewRequest(method, "/api/files", nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want %d", method, recorder.Code, http.StatusNoContent)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		maintenanceMode(true, next).ServeHTTP(recorder, httptest.NewRequest(method, "/internal/pubsub/shares", nil))
		if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" {
			t.Fatalf("%s response = %d, Retry-After %q", method, recorder.Code, recorder.Header().Get("Retry-After"))
		}
	}
}
