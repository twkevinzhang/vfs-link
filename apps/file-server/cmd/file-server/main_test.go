package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

func TestProductionDoesNotScheduleThumbnailGarbageCollection(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	forbiddenIdentifiers := map[string]struct{}{
		"startThumbnailGarbageCollector": {},
		"thumbnailGCInitialDelay":        {},
		"thumbnailGCInterval":            {},
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.Ident:
				if _, forbidden := forbiddenIdentifiers[typed.Name]; forbidden {
					t.Errorf("%s still contains automatic thumbnail GC symbol %q", name, typed.Name)
				}
			case *ast.CallExpr:
				if function, ok := typed.Fun.(*ast.Ident); ok && function.Name == "cleanupExpiredThumbnailObjects" {
					t.Errorf("%s still invokes thumbnail garbage collection from production code", name)
				}
			}
			return true
		})
	}
}

func TestEnsureNoLegacyUploadSessionsBlocksMutableKeys(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUpload(ctx, db.UploadRecord{
		ID: "legacy", LogicPath: "report.txt", PhysicalHash: "report.txt", Status: "uploading", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err = ensureNoLegacyUploadSessions(ctx, store); err == nil {
		t.Fatal("legacy rollout preflight error = nil")
	}
}

func TestEnsureNoLegacyUploadSessionsAcceptsImmutableKeys(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key, err := objectkey.ForUpload("report.txt", "new-upload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUpload(ctx, db.UploadRecord{
		ID: "new-upload", LogicPath: "report.txt", PhysicalHash: key, Status: "uploaded", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err = ensureNoLegacyUploadSessions(ctx, store); err != nil {
		t.Fatal(err)
	}
}

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

func TestNewMetadataStoreV4JSONLocal(t *testing.T) {
	root := t.TempDir()
	store, err := newMetadataStore(context.Background(), config.Config{
		DatabaseDriver:          "json",
		MetadataStorageDriver:   "local",
		MetadataLocalRoot:       root,
		MetadataPrefix:          "_vfs-link-v4",
		MetadataShardCount:      64,
		MetadataReducerInterval: 2 * time.Second,
		MetadataMutationMode:    "scoped",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "docs/report.txt", "object.txt", 4); err != nil {
		t.Fatal(err)
	}
	if record, found, err := store.Find(ctx, "docs/report.txt"); err != nil || !found || record.Size != 4 {
		t.Fatalf("Find() = %#v, %t, %v", record, found, err)
	}
	if _, err := os.Stat(filepath.Join(root, "_vfs-link-v4", "v4", "root.json")); err != nil {
		t.Fatalf("v4 metadata root: %v", err)
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
