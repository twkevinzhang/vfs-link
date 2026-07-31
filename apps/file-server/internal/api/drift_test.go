package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func TestDriftDefaultIsReadOnlyAndMissingSnapshotDoesNotTriggerScan(t *testing.T) {
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(metadata, objects, nil, "", "").Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/drift", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body driftSnapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.Enabled || !body.ReadOnly || body.SnapshotStatus != "missing" {
		t.Fatalf("unexpected response: %+v", body)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/drift/plans", nil)
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusForbidden {
		t.Fatalf("POST status = %d, want 403", postResponse.Code)
	}
}

func TestDriftActionsRejectNonGCSStorageEvenWhenEnabled(t *testing.T) {
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(metadata, objects, nil, "", "").SetDriftEnabled(true).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/drift/plans", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("POST status = %d, want 501; body=%s", response.Code, response.Body.String())
	}
}
