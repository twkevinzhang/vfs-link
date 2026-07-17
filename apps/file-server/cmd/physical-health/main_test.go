package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func TestResolveStorageConfig(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", "")
	t.Setenv("LOCAL_STORAGE_ROOT", "")
	t.Setenv("GCS_BUCKET", "")

	local, err := resolveStorageConfig("local", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if local.Driver != storageDriverLocal || !filepath.IsAbs(local.LocalRoot) {
		t.Fatalf("unexpected local config: %#v", local)
	}

	gcs, err := resolveStorageConfig("gcs", "", "objects-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if gcs.Driver != storageDriverGCS || gcs.GCSBucket != "objects-bucket" {
		t.Fatalf("unexpected GCS config: %#v", gcs)
	}

	if _, err := resolveStorageConfig("gcs", "", ""); err == nil {
		t.Fatal("expected missing GCS_BUCKET error")
	}
	if _, err := resolveStorageConfig("other", "", ""); err == nil {
		t.Fatal("expected unsupported driver error")
	}
}

func TestOpenMetadataStoreJSONLocal(t *testing.T) {
	root := t.TempDir()
	store, err := openMetadataStore(context.Background(), "json", "", "local", root, "", "_vfs-link")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "_vfs-link", "stats.json")); err != nil {
		t.Fatalf("tree metadata was not created: %v", err)
	}
}

func TestOpenMetadataStoreTreeV2AndValidateAggregates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := openMetadataStore(ctx, "json", "", "local", root, "", "_vfs-link-v2")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	records := []db.FileRecord{
		{ID: 1, LogicPath: "/docs", IsDirectory: true},
		{ID: 2, LogicPath: "/docs/a.txt", PhysicalHash: "same", Size: 4},
		{ID: 3, LogicPath: "/docs/b.txt", PhysicalHash: "same", Size: 4},
	}
	if _, err := db.BulkImportTree(ctx, store, db.TreeImportSnapshot{Records: records, NextFileID: 4}); err != nil {
		t.Fatal(err)
	}
	summary, err := validateMetadataAggregates(ctx, store, records)
	if err != nil {
		t.Fatal(err)
	}
	if summary != (db.FolderSummary{Files: 2, Directories: 1, Bytes: 8}) {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestOpenMetadataStoreValidation(t *testing.T) {
	if _, err := openMetadataStore(context.Background(), "postgres", "", "", "", "", ""); err == nil {
		t.Fatal("expected missing DATABASE_URL error")
	}
	if _, err := openMetadataStore(context.Background(), "json", "", "local", t.TempDir(), "", "metadata"); err == nil {
		t.Fatal("expected reserved JSON path error")
	}
}

func TestCheckLocal(t *testing.T) {
	root := t.TempDir()
	objectPath := filepath.Join(root, "abc")
	if err := os.WriteFile(objectPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		location     string
		expectedSize int64
		status       string
	}{
		{name: "healthy", location: objectPath, expectedSize: 4, status: statusOK},
		{name: "size mismatch", location: objectPath, expectedSize: 3, status: statusSizeMismatch},
		{name: "missing", location: filepath.Join(root, "missing"), expectedSize: 4, status: statusObjectMiss},
		{name: "directory", location: root, expectedSize: 4, status: statusStorageError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := healthRow{ObjectLocation: test.location, ExpectedSize: test.expectedSize}
			checkLocal(&row)
			if row.Status != test.status {
				t.Fatalf("status = %q, want %q", row.Status, test.status)
			}
		})
	}
}

func TestObjectLocation(t *testing.T) {
	local := storageConfig{Driver: storageDriverLocal, LocalRoot: "/objects"}
	if got := objectLocation(local, "/abc"); got != "/objects/abc" {
		t.Fatalf("local location = %q", got)
	}

	gcs := storageConfig{Driver: storageDriverGCS, GCSBucket: "bucket"}
	if got := objectLocation(gcs, "/abc"); got != "gs://bucket/abc" {
		t.Fatalf("GCS location = %q", got)
	}
}

func TestCheckGCS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/healthy.txt"):
			fmt.Fprint(w, `{"bucket":"objects","name":"healthy.txt","size":"4"}`)
		case strings.HasSuffix(r.URL.Path, "/missing.txt"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)

	rows := []healthRow{
		{PhysicalHash: "healthy.txt", ExpectedSize: 4},
		{PhysicalHash: "missing.txt", ExpectedSize: 4},
	}
	checkGCS(context.Background(), rows, "objects", 2)

	if rows[0].Status != statusOK || rows[0].ObjectSize != 4 {
		t.Fatalf("healthy row = %#v", rows[0])
	}
	if rows[1].Status != statusObjectMiss {
		t.Fatalf("missing row = %#v", rows[1])
	}
}

func TestClassify(t *testing.T) {
	if got := classify(statusOK); got != "healthy" {
		t.Fatalf("ok class = %q", got)
	}
	for _, status := range []string{statusObjectMiss, statusSizeMismatch, statusStorageError} {
		if got := classify(status); got != "unhealthy" {
			t.Fatalf("%s class = %q", status, got)
		}
	}
}

func TestWriteCSV(t *testing.T) {
	output := filepath.Join(t.TempDir(), "health.csv")
	rows := []healthRow{{
		LogicPath:      "/docs/report.pdf",
		ExpectedSize:   42,
		PhysicalHash:   "object.pdf",
		TopDir:         "docs",
		Status:         statusOK,
		Class:          "healthy",
		StorageDriver:  storageDriverGCS,
		ObjectLocation: "gs://bucket/object.pdf",
		ObjectSize:     42,
	}}

	if err := writeCSV(output, rows); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "logicPath,expectedSize,physicalHash") ||
		!strings.Contains(text, "/docs/report.pdf,42,object.pdf") {
		t.Fatalf("unexpected CSV content: %q", text)
	}
}
