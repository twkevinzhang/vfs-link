package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type blockingResumeStore struct {
	db.Store
	operations  db.TreeOperationStore
	listStarted chan struct{}
	allowList   chan struct{}
}

func (s *blockingResumeStore) CreateMoveOperation(ctx context.Context, paths []string, destination string) (db.OperationRecord, error) {
	return s.operations.CreateMoveOperation(ctx, paths, destination)
}
func (s *blockingResumeStore) CreateRenameOperation(ctx context.Context, path, name string) (db.OperationRecord, error) {
	return s.operations.CreateRenameOperation(ctx, path, name)
}
func (s *blockingResumeStore) CreateTrashOperation(ctx context.Context, paths []db.TrashPath) (db.OperationRecord, error) {
	return s.operations.CreateTrashOperation(ctx, paths)
}
func (s *blockingResumeStore) CreateRestoreOperation(ctx context.Context, ids []string) (db.OperationRecord, error) {
	return s.operations.CreateRestoreOperation(ctx, ids)
}
func (s *blockingResumeStore) CreateDeleteTrashOperation(ctx context.Context, ids []string) (db.OperationRecord, error) {
	return s.operations.CreateDeleteTrashOperation(ctx, ids)
}
func (s *blockingResumeStore) GetOperation(ctx context.Context, id string) (db.OperationRecord, bool, error) {
	return s.operations.GetOperation(ctx, id)
}
func (s *blockingResumeStore) ListRunnableOperations(ctx context.Context) ([]db.OperationRecord, error) {
	close(s.listStarted)
	select {
	case <-s.allowList:
		return s.operations.ListRunnableOperations(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *blockingResumeStore) RunOperation(ctx context.Context, id string) (db.OperationRecord, error) {
	return s.operations.RunOperation(ctx, id)
}
func (s *blockingResumeStore) RunDeleteTrashOperation(ctx context.Context, id string, executor func(context.Context, []string, func(int, int) error) (int64, error)) (db.OperationRecord, error) {
	return s.operations.RunDeleteTrashOperation(ctx, id, executor)
}

func TestMoveWaitsForInitialOperationResumeScan(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := metadata.UpsertDirectory(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	if err := metadata.UpsertFile(ctx, "a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	operations := metadata.(db.TreeOperationStore)
	store := &blockingResumeStore{
		Store:       metadata,
		operations:  operations,
		listStarted: make(chan struct{}),
		allowList:   make(chan struct{}),
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := New(store, objects)
	select {
	case <-store.listStarted:
	case <-time.After(time.Second):
		t.Fatal("initial operation resume scan did not start")
	}

	result := make(chan error, 1)
	go func() {
		_, moveErr := service.Move(ctx, []string{"a.txt"}, "target")
		result <- moveErr
	}()
	select {
	case err := <-result:
		t.Fatalf("Move() returned before resume scan completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(store.allowList)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Move() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Move() did not complete after resume scan")
	}
}

type failDeleteOnceStore struct {
	blob.Store
	failed bool
}

func (s *failDeleteOnceStore) Delete(ctx context.Context, physicalHash string) error {
	if !s.failed {
		s.failed = true
		return errors.New("temporary delete failure")
	}
	return s.Store.Delete(ctx, physicalHash)
}

func TestDeletePermanentlyDeletesObjectBeforeMetadata(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
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
	if err := metadata.UpsertFile(ctx, "a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	service := New(metadata, objects)
	trashResult, err := service.Trash(ctx, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	deleteResult, err := service.DeletePermanently(ctx, []string{trashResult.Records[0].TrashID})
	if err != nil || deleteResult.Deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleteResult.Deleted, err)
	}
	if _, err := os.Stat(filepath.Join(objects.Root(), "object-a")); !os.IsNotExist(err) {
		t.Fatalf("object stat err=%v", err)
	}
	if records, err := metadata.ListTrashRecords(ctx, nil); err != nil || len(records) != 0 {
		t.Fatalf("trash records=%d err=%v", len(records), err)
	}
}

func TestDeletePermanentlyCanRetryAfterObjectFailure(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
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
	if err := metadata.UpsertFile(ctx, "a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	store := &failDeleteOnceStore{Store: objects}
	service := New(metadata, store)
	trashResult, err := service.Trash(ctx, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	trashID := trashResult.Records[0].TrashID
	if _, err := service.DeletePermanently(ctx, []string{trashID}); err == nil {
		t.Fatal("first DeletePermanently() error = nil")
	}
	if _, err := service.Restore(ctx, []string{trashID}); !errors.Is(err, db.ErrTrashBusy) {
		t.Fatalf("Restore() during claimed deletion error = %v", err)
	}
	deleteResult, err := service.DeletePermanently(ctx, []string{trashID})
	if err != nil || deleteResult.Deleted != 1 {
		t.Fatalf("retry deleted=%d err=%v", deleteResult.Deleted, err)
	}
}

func TestDeletePermanentlyCanRetryAfterCheckpointFailure(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
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
	if err := metadata.UpsertFile(ctx, "a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	service := New(metadata, objects)
	trashResult, err := service.Trash(ctx, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	trashID := trashResult.Records[0].TrashID
	checkpointErr := errors.New("checkpoint unavailable")
	if _, err := service.deletePermanentlyWithCheckpoint(ctx, []string{trashID}, func(done, total int) error {
		if done != 1 || total != 1 {
			t.Fatalf("checkpoint done=%d total=%d", done, total)
		}
		return checkpointErr
	}); !errors.Is(err, checkpointErr) {
		t.Fatalf("first deletion error = %v", err)
	}
	if records, err := metadata.ListTrashRecords(ctx, []string{trashID}); err != nil || len(records) == 0 {
		t.Fatalf("trash records after checkpoint failure=%d err=%v", len(records), err)
	}
	result, err := service.DeletePermanently(ctx, []string{trashID})
	if err != nil || result.Deleted != 1 {
		t.Fatalf("retry deleted=%d err=%v", result.Deleted, err)
	}
}
