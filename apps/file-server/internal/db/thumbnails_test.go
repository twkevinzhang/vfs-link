package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
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
	if len(orphans) != 0 {
		t.Fatalf("detach must defer thumbnail deletion = %#v", orphans)
	}
	thumbA, found, err := store.FindThumbnail(ctx, "thumb-a")
	if err != nil || !found || thumbA.DeleteAfter == nil {
		t.Fatalf("thumb-a must be retained as GC candidate: %#v found=%v err=%v", thumbA, found, err)
	}
	collector := store.(ThumbnailGarbageCollector)
	deleted, err := collector.CleanupExpiredThumbnails(ctx, thumbA.DeleteAfter.Add(time.Second), func(_ context.Context, thumbnail ThumbnailRecord) error {
		if thumbnail.ID != "thumb-a" {
			t.Fatalf("unexpected thumbnail deletion: %#v", thumbnail)
		}
		return nil
	})
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup thumb-a = %d, %v", deleted, err)
	}
	orphans, err = store.DetachThumbnails(ctx, []int{first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("detach must defer thumbnail deletion = %#v", orphans)
	}
	thumbB, found, err := store.FindThumbnail(ctx, "thumb-b")
	if err != nil || !found || thumbB.DeleteAfter == nil {
		t.Fatalf("thumb-b must be retained as GC candidate: %#v found=%v err=%v", thumbB, found, err)
	}
	deleted, err = collector.CleanupExpiredThumbnails(ctx, thumbB.DeleteAfter.Add(thumbnailGCMinInterval+time.Second), func(_ context.Context, thumbnail ThumbnailRecord) error {
		if thumbnail.ID != "thumb-b" {
			t.Fatalf("unexpected thumbnail deletion: %#v", thumbnail)
		}
		return nil
	})
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup thumb-b = %d, %v", deleted, err)
	}
}

type thumbnailCountingBackend struct {
	inner treeBackend
	mu    sync.Mutex
	gets  int
	puts  int
	dels  int
	lists int
}

func (b *thumbnailCountingBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	b.mu.Lock()
	b.gets++
	b.mu.Unlock()
	return b.inner.Get(ctx, key)
}
func (b *thumbnailCountingBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	b.mu.Lock()
	b.puts++
	b.mu.Unlock()
	return b.inner.Put(ctx, key, data, expected)
}
func (b *thumbnailCountingBackend) Delete(ctx context.Context, key string, expected *int64) error {
	b.mu.Lock()
	b.dels++
	b.mu.Unlock()
	return b.inner.Delete(ctx, key, expected)
}
func (b *thumbnailCountingBackend) List(ctx context.Context, prefix string) ([]string, error) {
	b.mu.Lock()
	b.lists++
	b.mu.Unlock()
	return b.inner.List(ctx, prefix)
}
func (b *thumbnailCountingBackend) Close() error { return b.inner.Close() }
func (b *thumbnailCountingBackend) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets, b.puts, b.dels, b.lists = 0, 0, 0, 0
}
func (b *thumbnailCountingBackend) counts() (gets, puts, dels, lists int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gets, b.puts, b.dels, b.lists
}

func TestTreeThumbnailIndexAvoidsCollectionScansForMissingAndReplacement(t *testing.T) {
	ctx := context.Background()
	inner, err := newLocalTreeBackend(filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	backend := &thumbnailCountingBackend{inner: inner}
	store := newTreeStore(backend, "")
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err = store.putEntity(ctx, "thumbnails", fmt.Sprintf("unrelated-%04d", i), ThumbnailRecord{ID: fmt.Sprintf("unrelated-%04d", i), PhysicalHash: fmt.Sprintf("unrelated-%04d.webp", i)}, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"without-a", "without-b", "target"} {
		if err = store.UpsertFile(ctx, path, "object-"+path, 1); err != nil {
			t.Fatal(err)
		}
	}
	target, found, err := store.Find(ctx, "target")
	if err != nil || !found {
		t.Fatalf("target found=%v err=%v", found, err)
	}
	backend.reset()
	links, err := store.FindThumbnailsForFiles(ctx, []int{target.ID})
	if err != nil || len(links) != 0 {
		t.Fatalf("missing thumbnail lookup = %#v, %v", links, err)
	}
	gets, puts, dels, lists := backend.counts()
	if lists != 0 || gets > 2 || puts != 0 || dels != 0 {
		t.Fatalf("missing-thumbnail lookup operations get=%d put=%d delete=%d list=%d", gets, puts, dels, lists)
	}
	backend.reset()
	if _, err = store.ReplaceThumbnail(ctx, ThumbnailRecord{ID: "target-thumb", PhysicalHash: "target.webp"}, []int{target.ID}); err != nil {
		t.Fatal(err)
	}
	gets, puts, dels, lists = backend.counts()
	if lists != 0 || gets > 8 || puts > 5 || dels > 1 {
		t.Fatalf("replacement operations get=%d put=%d delete=%d list=%d", gets, puts, dels, lists)
	}
	backend.reset()
	if _, err = store.DetachThumbnails(ctx, []int{target.ID}); err != nil {
		t.Fatal(err)
	}
	gets, puts, dels, lists = backend.counts()
	if lists != 0 || gets > 8 || puts > 2 || dels > 2 {
		t.Fatalf("detach operations get=%d put=%d delete=%d list=%d", gets, puts, dels, lists)
	}
}

func TestRebuildTreeThumbnailIndexRepairsLegacyRecords(t *testing.T) {
	ctx := context.Background()
	storeRaw, err := NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	store := storeRaw.(*TreeStore)
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertFile(ctx, "legacy.zip", "legacy-object", 1); err != nil {
		t.Fatal(err)
	}
	file, found, err := store.Find(ctx, "legacy.zip")
	if err != nil || !found {
		t.Fatalf("legacy file found=%v err=%v", found, err)
	}
	if err = store.putEntity(ctx, "thumbnails", "legacy-old", ThumbnailRecord{ID: "legacy-old", PhysicalHash: "legacy-old.webp", FileIDs: []int{file.ID}, CreatedAt: time.Now().UTC().Add(-time.Hour)}, true); err != nil {
		t.Fatal(err)
	}
	if err = store.putEntity(ctx, "thumbnails", "legacy-thumb", ThumbnailRecord{ID: "legacy-thumb", PhysicalHash: "legacy.webp", FileIDs: []int{file.ID}, CreatedAt: time.Now().UTC()}, true); err != nil {
		t.Fatal(err)
	}
	before, err := store.FindThumbnailsForFiles(ctx, []int{file.ID})
	if err != nil || len(before) != 0 {
		t.Fatalf("legacy record must not trigger request-path scan: %#v, %v", before, err)
	}
	if err = RebuildTreeThumbnailIndex(ctx, store); err != nil {
		t.Fatal(err)
	}
	after, err := store.FindThumbnailsForFiles(ctx, []int{file.ID})
	if err != nil || after[file.ID].ID != "legacy-thumb" {
		t.Fatalf("rebuilt thumbnail link = %#v, %v", after, err)
	}
}

func TestTreeThumbnailGarbageCollectionMarksOrphansAndRetriesObjectFailures(t *testing.T) {
	ctx := context.Background()
	storeRaw, err := NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	store := storeRaw.(*TreeStore)
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	thumbnail := ThumbnailRecord{ID: "interrupted-orphan", PhysicalHash: "orphan.webp", FileIDs: []int{999}, CreatedAt: createdAt}
	if err = store.putEntity(ctx, "thumbnails", thumbnail.ID, thumbnail, true); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.CleanupExpiredThumbnails(ctx, createdAt, func(context.Context, ThumbnailRecord) error {
		t.Fatal("newly discovered orphan must observe the grace period")
		return nil
	})
	if err != nil || deleted != 0 {
		t.Fatalf("mark orphan deleted=%d err=%v", deleted, err)
	}
	marked, found, err := store.FindThumbnail(ctx, thumbnail.ID)
	if err != nil || !found || marked.DeleteAfter == nil || !marked.DeleteAfter.Equal(createdAt.Add(7*24*time.Hour)) || len(marked.FileIDs) != 0 {
		t.Fatalf("marked orphan=%#v found=%v err=%v", marked, found, err)
	}

	deleteErr := errors.New("temporary object deletion failure")
	deleted, err = store.CleanupExpiredThumbnails(ctx, marked.DeleteAfter.Add(time.Second), func(context.Context, ThumbnailRecord) error {
		return deleteErr
	})
	if !errors.Is(err, deleteErr) || deleted != 0 {
		t.Fatalf("failed cleanup deleted=%d err=%v", deleted, err)
	}
	if _, found, err = store.FindThumbnail(ctx, thumbnail.ID); err != nil || !found {
		t.Fatalf("metadata must remain retryable: found=%v err=%v", found, err)
	}

	deleted, err = store.CleanupExpiredThumbnails(ctx, marked.DeleteAfter.Add(time.Second), func(_ context.Context, candidate ThumbnailRecord) error {
		if candidate.ID != thumbnail.ID {
			t.Fatalf("unexpected candidate: %#v", candidate)
		}
		return nil
	})
	if err != nil || deleted != 1 {
		t.Fatalf("retry cleanup deleted=%d err=%v", deleted, err)
	}
	if _, found, err = store.FindThumbnail(ctx, thumbnail.ID); err != nil || found {
		t.Fatalf("metadata remains after successful retry: found=%v err=%v", found, err)
	}
}
