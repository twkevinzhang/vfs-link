package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

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
	if err := metadata.UpsertFile(ctx, "/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	service := New(metadata, objects)
	trashResult, err := service.Trash(ctx, []string{"/a.txt"})
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
	if err := metadata.UpsertFile(ctx, "/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	store := &failDeleteOnceStore{Store: objects}
	service := New(metadata, store)
	trashResult, err := service.Trash(ctx, []string{"/a.txt"})
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
	if err := metadata.UpsertFile(ctx, "/a.txt", "object-a", 1); err != nil {
		t.Fatal(err)
	}
	service := New(metadata, objects)
	trashResult, err := service.Trash(ctx, []string{"/a.txt"})
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
