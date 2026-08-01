package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTreeThumbnailReplacementPreservesSharedReferences(t *testing.T) {
	ctx := context.Background()
	store, err := NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"archive.z01", "archive.zip"} {
		if err = store.UpsertFile(ctx, path, "object-"+path, 10); err != nil {
			t.Fatal(err)
		}
	}
	first, _, err := store.Find(ctx, "archive.z01")
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := store.Find(ctx, "archive.zip")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	orphans, err := store.ReplaceThumbnail(ctx, ThumbnailRecord{ID: "thumb-a", PhysicalHash: "thumb-a.webp", ContentType: "image/webp", Size: 12, Width: 10, Height: 8, CreatedAt: now}, []int{first.ID, last.ID})
	if err != nil || len(orphans) != 0 {
		t.Fatalf("initial replace = %#v, %v", orphans, err)
	}
	linked, err := store.FindThumbnailsForFiles(ctx, []int{first.ID, last.ID})
	if err != nil {
		t.Fatal(err)
	}
	if linked[first.ID].ID != "thumb-a" || linked[last.ID].ID != "thumb-a" {
		t.Fatalf("links = %#v", linked)
	}
	orphans, err = store.ReplaceThumbnail(ctx, ThumbnailRecord{ID: "thumb-b", PhysicalHash: "thumb-b.webp", ContentType: "image/webp", Size: 13, Width: 9, Height: 9, CreatedAt: now}, []int{first.ID})
	if err != nil || len(orphans) != 0 {
		t.Fatalf("partial replace removed shared thumbnail: %#v, %v", orphans, err)
	}
	orphans, err = store.DetachThumbnails(ctx, []int{last.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ID != "thumb-a" {
		t.Fatalf("detach last thumb-a reference = %#v", orphans)
	}
	orphans, err = store.DetachThumbnails(ctx, []int{first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ID != "thumb-b" {
		t.Fatalf("detach last thumb-b reference = %#v", orphans)
	}
}
