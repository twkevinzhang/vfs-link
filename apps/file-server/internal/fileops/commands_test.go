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
