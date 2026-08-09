package fileops

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func newCommandFixture(t *testing.T) (context.Context, db.Store, *blob.LocalStore, *Service) {
	t.Helper()
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err = metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	return ctx, metadata, objects, New(metadata, objects, objects)
}

func TestRelocateUsesExactTargetAndPreservesObject(t *testing.T) {
	ctx, metadata, _, service := newCommandFixture(t)
	if _, err := service.CreateDirectory(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateDirectory(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	if err := metadata.UpsertFile(ctx, "source/a.txt", "stable-object", 7); err != nil {
		t.Fatal(err)
	}
	result, err := service.Relocate(ctx, "source/a.txt", "target/renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].LogicPath != "target/renamed.txt" || result.Records[0].PhysicalHash != "stable-object" {
		t.Fatalf("relocate result=%+v", result)
	}
	if _, found, err := metadata.Find(ctx, "source/a.txt"); err != nil || found {
		t.Fatalf("source found=%t err=%v", found, err)
	}
}

func TestDeleteToTrashPreservesRestorableMapping(t *testing.T) {
	ctx, metadata, objects, service := newCommandFixture(t)
	writer, err := objects.NewWriter(ctx, "object-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = metadata.UpsertFile(ctx, "a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	result, err := service.DeleteToTrash(ctx, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].TrashID == "" {
		t.Fatalf("trash result=%+v", result)
	}
	if reader, err := objects.NewReader(ctx, "object-a"); err != nil {
		t.Fatalf("trash deleted archive object: %v", err)
	} else {
		_ = reader.Close()
	}
	if _, err = service.Restore(ctx, []string{result.Records[0].TrashID}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := metadata.Find(ctx, "a.txt"); err != nil || !found {
		t.Fatalf("restored found=%t err=%v", found, err)
	}
}

type alwaysFailDeleteStore struct{ blob.Store }

func (s alwaysFailDeleteStore) Delete(context.Context, string) error {
	return errors.New("injected cleanup failure")
}

func TestPublishUploadedIsIdempotentAndCleanupFailureDoesNotUndoCommit(t *testing.T) {
	ctx, metadata, objects, _ := newCommandFixture(t)
	if err := metadata.UpsertFile(ctx, "a.txt", "old-object", 3); err != nil {
		t.Fatal(err)
	}
	expected := "old-object"
	service := New(metadata, alwaysFailDeleteStore{Store: objects}, objects)
	intent := PublishIntent{
		LogicPath: "a.txt", PhysicalHash: "new-object", Size: 7,
		ExpectedPhysicalHash: &expected,
	}
	result, err := service.PublishUploaded(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CleanupPending || result.Published.PhysicalHash != "new-object" {
		t.Fatalf("publish result=%+v", result)
	}
	retry, err := service.PublishUploaded(ctx, PublishIntent{
		LogicPath: "a.txt", PhysicalHash: "new-object", Size: 7,
		RequireAbsent: true,
	})
	if err != nil || retry.Published.PhysicalHash != "new-object" {
		t.Fatalf("idempotent retry=%+v err=%v", retry, err)
	}
}

func TestPublishUploadedSnapshotRejectsStaleSameKeyAndConvergesAfterAmbiguousCommit(t *testing.T) {
	ctx, metadata, _, service := newCommandFixture(t)
	if err := metadata.UpsertFile(ctx, "a.txt", "old-object", 3); err != nil {
		t.Fatal(err)
	}
	original, found, err := metadata.Find(ctx, "a.txt")
	if err != nil || !found {
		t.Fatalf("find original=%+v found=%t err=%v", original, found, err)
	}
	snapshot := original.Snapshot()
	expectedPhysicalHash := "old-object"
	intent := PublishIntent{
		LogicPath: "a.txt", PhysicalHash: "uploads/upload-1", Size: 7,
		ExpectedPhysicalHash: &expectedPhysicalHash, ExpectedSnapshot: &snapshot, DeferCleanup: true,
	}
	first, err := service.PublishUploaded(ctx, intent)
	if err != nil || first.PreviousObject != "old-object" || !first.CleanupPending {
		t.Fatalf("first publish=%+v err=%v", first, err)
	}
	retry, err := service.PublishUploaded(ctx, intent)
	if err != nil || retry.Published.PhysicalHash != "uploads/upload-1" || retry.PreviousObject != "old-object" || !retry.CleanupPending {
		t.Fatalf("ambiguous retry=%+v err=%v", retry, err)
	}

	if err = metadata.UpsertFile(ctx, "stale.txt", "same-key", 5); err != nil {
		t.Fatal(err)
	}
	staleRecord, _, err := metadata.Find(ctx, "stale.txt")
	if err != nil {
		t.Fatal(err)
	}
	staleSnapshot := staleRecord.Snapshot()
	time.Sleep(time.Millisecond)
	if _, err = metadata.ReplaceFile(ctx, "stale.txt", "same-key", 5); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PublishUploaded(ctx, PublishIntent{
		LogicPath: "stale.txt", PhysicalHash: "uploads/upload-2", Size: 5,
		ExpectedSnapshot: &staleSnapshot, DeferCleanup: true,
	}); !errors.Is(err, db.ErrPathConflict) {
		t.Fatalf("stale same-key snapshot error=%v, want ErrPathConflict", err)
	}
}

func putTestObject(t *testing.T, ctx context.Context, store blob.Store, key, value string) {
	t.Helper()
	writer, err := store.NewWriter(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func requireTestObject(t *testing.T, ctx context.Context, store blob.Store, key string, exists bool) {
	t.Helper()
	reader, err := store.NewReader(ctx, key)
	if exists {
		if err != nil {
			t.Fatalf("open object %q: %v", key, err)
		}
		_ = reader.Close()
		return
	}
	if err == nil {
		_ = reader.Close()
		t.Fatalf("object %q still exists", key)
	}
}

func TestRetryUploadedCleanupPreservesReferencedObjects(t *testing.T) {
	t.Run("active mapping", func(t *testing.T) {
		ctx, metadata, objects, service := newCommandFixture(t)
		putTestObject(t, ctx, objects, "previous", "old")
		if err := metadata.UpsertFile(ctx, "published.txt", "published", 3); err != nil {
			t.Fatal(err)
		}
		if err := metadata.UpsertFile(ctx, "alias.txt", "previous", 3); err != nil {
			t.Fatal(err)
		}
		result := service.RetryUploadedCleanup(ctx, UploadedCleanupIntent{
			LogicPath: "published.txt", PublishedPhysicalHash: "published", PreviousPhysicalHash: "previous",
		})
		if !result.Pending {
			t.Fatalf("cleanup result=%+v, want pending", result)
		}
		requireTestObject(t, ctx, objects, "previous", true)
	})

	t.Run("trash mapping", func(t *testing.T) {
		ctx, metadata, objects, service := newCommandFixture(t)
		putTestObject(t, ctx, objects, "previous", "old")
		if err := metadata.UpsertFile(ctx, "published.txt", "published", 3); err != nil {
			t.Fatal(err)
		}
		if err := metadata.UpsertFile(ctx, "trash-me.txt", "previous", 3); err != nil {
			t.Fatal(err)
		}
		if _, err := metadata.TrashPaths(ctx, []db.TrashPath{{Path: "trash-me.txt", TrashID: "trash-ref"}}); err != nil {
			t.Fatal(err)
		}
		result := service.RetryUploadedCleanup(ctx, UploadedCleanupIntent{
			LogicPath: "published.txt", PublishedPhysicalHash: "published", PreviousPhysicalHash: "previous",
		})
		if !result.Pending {
			t.Fatalf("cleanup result=%+v, want pending", result)
		}
		requireTestObject(t, ctx, objects, "previous", true)
	})

	t.Run("pending share", func(t *testing.T) {
		ctx, metadata, objects, service := newCommandFixture(t)
		putTestObject(t, ctx, objects, "previous", "old")
		if err := metadata.UpsertFile(ctx, "published.txt", "published", 3); err != nil {
			t.Fatal(err)
		}
		share, err := metadata.CreateShare(ctx, db.ShareRecord{
			ID: "share-ref", LogicPath: "old.txt", PhysicalHash: "previous", FileName: "old.txt",
			DestinationObject: "shares/old.txt", ShareURL: "https://example.test/old.txt", Status: "draft",
		})
		if err != nil {
			t.Fatal(err)
		}
		intent := UploadedCleanupIntent{LogicPath: "published.txt", PublishedPhysicalHash: "published", PreviousPhysicalHash: "previous"}
		if result := service.RetryUploadedCleanup(ctx, intent); !result.Pending {
			t.Fatalf("pending-share cleanup=%+v", result)
		}
		requireTestObject(t, ctx, objects, "previous", true)
		if _, err = metadata.MarkShareUploaded(ctx, share.ID); err != nil {
			t.Fatal(err)
		}
		if result := service.RetryUploadedCleanup(ctx, intent); result.Pending {
			t.Fatalf("completed-share cleanup=%+v", result)
		}
		requireTestObject(t, ctx, objects, "previous", false)
	})
}

type failOnceDeleteStore struct {
	blob.Store
	failed bool
}

func (s *failOnceDeleteStore) Delete(ctx context.Context, key string) error {
	if !s.failed {
		s.failed = true
		return errors.New("injected first delete failure")
	}
	return s.Store.Delete(ctx, key)
}

func TestRetryUploadedCleanupRetriesObjectDeletion(t *testing.T) {
	ctx, metadata, objects, _ := newCommandFixture(t)
	putTestObject(t, ctx, objects, "previous", "old")
	if err := metadata.UpsertFile(ctx, "published.txt", "published", 3); err != nil {
		t.Fatal(err)
	}
	failing := &failOnceDeleteStore{Store: objects}
	service := New(metadata, failing, objects)
	intent := UploadedCleanupIntent{LogicPath: "published.txt", PublishedPhysicalHash: "published", PreviousPhysicalHash: "previous"}
	if result := service.RetryUploadedCleanup(ctx, intent); !result.Pending {
		t.Fatalf("first cleanup=%+v, want pending", result)
	}
	if result := service.RetryUploadedCleanup(ctx, intent); result.Pending {
		t.Fatalf("second cleanup=%+v", result)
	}
	requireTestObject(t, ctx, objects, "previous", false)
}

type failDetachStore struct{ db.Store }

func (failDetachStore) DetachThumbnails(context.Context, []int) ([]db.ThumbnailRecord, error) {
	return nil, errors.New("injected detach failure")
}

func TestRetryUploadedCleanupDoesNotDeleteThumbnailBeforeDetach(t *testing.T) {
	ctx, metadata, objects, _ := newCommandFixture(t)
	if err := metadata.UpsertFile(ctx, "published.txt", "published", 3); err != nil {
		t.Fatal(err)
	}
	published, _, err := metadata.Find(ctx, "published.txt")
	if err != nil {
		t.Fatal(err)
	}
	putTestObject(t, ctx, objects, "thumbnail-object", "thumb")
	if _, err = metadata.ReplaceThumbnail(ctx, db.ThumbnailRecord{
		ID: "thumb", PhysicalHash: "thumbnail-object", ContentType: "image/webp", Size: 5,
		Width: 1, Height: 1,
	}, []int{published.ID}); err != nil {
		t.Fatal(err)
	}
	service := New(failDetachStore{Store: metadata}, objects, objects)
	result := service.RetryUploadedCleanup(ctx, UploadedCleanupIntent{
		LogicPath: "published.txt", PublishedPhysicalHash: "published",
	})
	if !result.Pending {
		t.Fatalf("cleanup=%+v, want pending", result)
	}
	requireTestObject(t, ctx, objects, "thumbnail-object", true)
}

type pendingOperationStore struct {
	db.Store
	operation db.OperationRecord
}

func (s *pendingOperationStore) CreateMoveOperation(context.Context, []string, string) (db.OperationRecord, error) {
	return s.operation, nil
}
func (s *pendingOperationStore) CreateRenameOperation(context.Context, string, string) (db.OperationRecord, error) {
	return s.operation, nil
}
func (s *pendingOperationStore) CreateTrashOperation(context.Context, []db.TrashPath) (db.OperationRecord, error) {
	return s.operation, nil
}
func (s *pendingOperationStore) CreateRestoreOperation(context.Context, []string) (db.OperationRecord, error) {
	return s.operation, nil
}
func (s *pendingOperationStore) CreateDeleteTrashOperation(context.Context, []string) (db.OperationRecord, error) {
	return s.operation, nil
}
func (s *pendingOperationStore) GetOperation(context.Context, string) (db.OperationRecord, bool, error) {
	return s.operation, true, nil
}
func (*pendingOperationStore) ListRunnableOperations(context.Context) ([]db.OperationRecord, error) {
	return nil, nil
}
func (s *pendingOperationStore) RunOperation(context.Context, string) (db.OperationRecord, error) {
	return s.operation, nil
}
func (s *pendingOperationStore) RunDeleteTrashOperation(context.Context, string, func(context.Context, []string, func(int, int) error) (int64, error)) (db.OperationRecord, error) {
	return s.operation, nil
}

func TestWaitVisibleTimeoutDoesNotMutateOperation(t *testing.T) {
	ctx, metadata, objects, _ := newCommandFixture(t)
	operation := db.OperationRecord{ID: "pending", Status: db.OperationStatusPending}
	store := &pendingOperationStore{Store: metadata, operation: operation}
	service := New(store, objects, objects)
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := service.WaitVisible(waitCtx, operation.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitVisible() error=%v", err)
	}
	if store.operation.Status != db.OperationStatusPending {
		t.Fatalf("operation status=%s", store.operation.Status)
	}
}
