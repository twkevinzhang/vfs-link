package vfs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func TestFSNameIsStorageDriverNeutral(t *testing.T) {
	fs := New(nil, nil)
	if got, want := fs.Name(), "vfs-link"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestCreatePublishesSanitizedFinalObjectKey(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	fs := New(store, objects)

	file, err := fs.Create("/docs/A:B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Find(ctx, "/docs/A:B.txt")
	if err != nil || !found {
		t.Fatalf("Find() = found %t, error %v", found, err)
	}
	if record.PhysicalHash != "docs/A_B.txt" {
		t.Fatalf("PhysicalHash = %q, want sanitized final key", record.PhysicalHash)
	}
}

func TestCreateRejectsSanitizerCollisionWithoutSuffixOrOverwrite(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	fs := New(store, objects)
	first, err := fs.Create("/docs/A?B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	colliding, err := fs.Create("/docs/A:B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := colliding.Write([]byte("second")); err == nil {
		t.Fatal("colliding Write() error = nil")
	}
	record, found, err := store.Find(ctx, "/docs/A?B.txt")
	if err != nil || !found || record.PhysicalHash != "docs/A_B.txt" {
		t.Fatalf("original record = %#v, found %t, error %v", record, found, err)
	}
}
