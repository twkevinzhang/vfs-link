package vfs

import (
	"context"
	"errors"
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

func TestMutationsUseSharedCommandsAndDeleteToTrash(t *testing.T) {
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
	if err := fs.Mkdir("/docs", 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := fs.Create("/docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = fs.Rename("/docs/a.txt", "/docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Find(ctx, "docs/b.txt")
	if err != nil || !found {
		t.Fatalf("renamed found=%t err=%v", found, err)
	}
	if err = fs.Remove("/docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, found, err = store.Find(ctx, "docs/b.txt"); err != nil || found {
		t.Fatalf("active mapping found=%t err=%v", found, err)
	}
	trash, err := store.ListTrash(ctx)
	if err != nil || len(trash) != 1 || trash[0].PhysicalHash != record.PhysicalHash {
		t.Fatalf("trash=%+v err=%v", trash, err)
	}
	if reader, err := objects.NewReader(ctx, record.PhysicalHash); err != nil {
		t.Fatalf("delete-to-trash removed object: %v", err)
	} else {
		_ = reader.Close()
	}
	if err = fs.Remove("/docs/missing.txt"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing remove error=%v", err)
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
	record, found, err := store.Find(ctx, "docs/A:B.txt")
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
	record, found, err := store.Find(ctx, "docs/A?B.txt")
	if err != nil || !found || record.PhysicalHash != "docs/A_B.txt" {
		t.Fatalf("original record = %#v, found %t, error %v", record, found, err)
	}
}
