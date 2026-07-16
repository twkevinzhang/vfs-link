package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
)

func TestNewMetadataStoreJSONLocal(t *testing.T) {
	root := t.TempDir()
	store, err := newMetadataStore(context.Background(), config.Config{
		DatabaseDriver:   "json",
		StorageDriver:    "local",
		LocalStorageRoot: root,
		JSONDBObject:     "_vfs-link/metadata.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(context.Background(), "/report.txt", "object.txt", 4); err != nil {
		t.Fatal(err)
	}
	if record, found, err := store.Find(context.Background(), "/report.txt"); err != nil || !found || record.Size != 4 {
		t.Fatalf("Find() = %#v, %t, %v", record, found, err)
	}
	if _, err := os.Stat(filepath.Join(root, "_vfs-link", "metadata.json")); err != nil {
		t.Fatalf("metadata file: %v", err)
	}
}
