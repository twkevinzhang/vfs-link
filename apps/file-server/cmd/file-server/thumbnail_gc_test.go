package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func TestCleanupExpiredThumbnailObjectsDeletesMetadataAndWebP(t *testing.T) {
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
	if err = store.UpsertFile(ctx, "archive.zip", "archive.zip", 1); err != nil {
		t.Fatal(err)
	}
	file, found, err := store.Find(ctx, "archive.zip")
	if err != nil || !found {
		t.Fatalf("file found=%v err=%v", found, err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := objects.NewWriter(ctx, "_vfs-link-thumbnails/thumb.webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("webp")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	record := db.ThumbnailRecord{ID: "thumb", PhysicalHash: "_vfs-link-thumbnails/thumb.webp", CreatedAt: time.Now().UTC()}
	if _, err = store.ReplaceThumbnail(ctx, record, []int{file.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DetachThumbnails(ctx, []int{file.ID}); err != nil {
		t.Fatal(err)
	}
	retained, found, err := store.FindThumbnail(ctx, record.ID)
	if err != nil || !found || retained.DeleteAfter == nil {
		t.Fatalf("retained=%#v found=%v err=%v", retained, found, err)
	}

	deleted, err := cleanupExpiredThumbnailObjects(ctx, store, objects, retained.DeleteAfter.Add(time.Second))
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if _, found, err = store.FindThumbnail(ctx, record.ID); err != nil || found {
		t.Fatalf("thumbnail metadata found=%v err=%v", found, err)
	}
	if reader, openErr := objects.NewReader(ctx, record.PhysicalHash); openErr == nil {
		_ = reader.Close()
		t.Fatal("expired thumbnail object still exists")
	}
}
