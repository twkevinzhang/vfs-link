package upload

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type unusedStorage struct{}

func (unusedStorage) Driver() string { return "test" }
func (unusedStorage) Prepare(context.Context, Session) (PreparedTarget, error) {
	panic("unexpected call")
}
func (unusedStorage) Write(context.Context, Session, io.Reader) (int64, error) {
	panic("unexpected call")
}
func (unusedStorage) Stat(context.Context, string) (int64, error) { panic("unexpected call") }
func (unusedStorage) Delete(context.Context, string) error        { panic("unexpected call") }
func (unusedStorage) Cancel(context.Context, Session) error       { panic("unexpected call") }

func TestCreateRejectsFileOverMaxBytesBeforeAllocatingSession(t *testing.T) {
	service := New(nil, nil, unusedStorage{}, WithMaxBytes(10))
	_, err := service.Create(context.Background(), CreateInput{LogicPath: "large.bin", Size: 11})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("Create() error = %v, want maximum size error", err)
	}
}

func TestDefaultMaxBytesIsFiftyGiB(t *testing.T) {
	if DefaultMaxBytes != 50*1024*1024*1024 {
		t.Fatalf("DefaultMaxBytes = %d", DefaultMaxBytes)
	}
}

func TestLocalUploadCreateWriteComplete(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "_vfs-link"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithBlob(store, objects)

	session, err := service.Create(ctx, CreateInput{LogicPath: "report.txt", Size: 4, ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if session.Driver != "local" || session.RequireAbsent != true {
		t.Fatalf("created session = %#v", session)
	}
	if session.PhysicalHash != "report.txt" {
		t.Fatalf("physical hash = %q, want final sanitized key", session.PhysicalHash)
	}
	if _, err := service.Write(ctx, session.ID, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	completed, err := service.Complete(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusComplete {
		t.Fatalf("status = %q", completed.Status)
	}
	record, found, err := store.Find(ctx, "report.txt")
	if err != nil || !found || record.Size != 4 {
		t.Fatalf("file mapping = %#v, %t, %v", record, found, err)
	}
}

func TestLocalFolderUploadCreatesParentDirectories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "_vfs-link"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithBlob(store, objects)

	session, err := service.Create(ctx, CreateInput{LogicPath: "photos/trip/day-1/image.txt", Size: 4, ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, session.ID, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}

	for _, logicPath := range []string{"photos", "photos/trip", "photos/trip/day-1"} {
		record, found, err := store.Find(ctx, logicPath)
		if err != nil || !found || !record.IsDirectory {
			t.Fatalf("directory %q = %#v, %t, %v", logicPath, record, found, err)
		}
	}
	record, found, err := store.Find(ctx, "photos/trip/day-1/image.txt")
	if err != nil || !found || record.IsDirectory || record.Size != 4 {
		t.Fatalf("file = %#v, %t, %v", record, found, err)
	}
}

func TestLocalUploadRejectsMoreThanDeclaredSize(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "_vfs-link"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithBlob(store, objects)
	session, err := service.Create(ctx, CreateInput{LogicPath: "short.txt", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, session.ID, strings.NewReader("data plus ignored tail")); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Write() error = %v", err)
	}
	listed, err := objects.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("objects after rejected upload = %#v", listed)
	}
}

func TestLocalOversizedOverwritePreservesExistingFinalObject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "_vfs-link"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithBlob(store, objects)
	first, err := service.Create(ctx, CreateInput{LogicPath: "report.txt", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, first.ID, strings.NewReader("safe")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}

	overwrite, err := service.Create(ctx, CreateInput{LogicPath: "report.txt", Size: 4, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, overwrite.ID, strings.NewReader("unsafe extra bytes")); err == nil {
		t.Fatal("oversized overwrite error = nil")
	}
	reader, err := objects.NewReader(ctx, "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "safe" {
		t.Fatalf("final content = %q, want original", content)
	}
}

type singleSessionRepository struct{ session Session }

func (r *singleSessionRepository) CreateUpload(_ context.Context, session Session) error {
	r.session = session
	return nil
}
func (r *singleSessionRepository) FindUpload(context.Context, string) (Session, bool, error) {
	return r.session, true, nil
}
func (r *singleSessionRepository) UpdateUpload(_ context.Context, session Session) error {
	r.session = session
	return nil
}
func (*singleSessionRepository) DeleteUpload(context.Context, string) error { return nil }

type retryPublisher struct{ calls int }

func (*retryPublisher) FindFile(context.Context, string) (File, bool, error) { panic("unused") }
func (*retryPublisher) EnsureDirectory(context.Context, string) error        { return nil }
func (p *retryPublisher) ReplaceFile(_ context.Context, _, physicalHash string, _ int64, _ *string, _ bool) (string, bool, error) {
	p.calls++
	if p.calls == 1 {
		return "", false, errors.New("transient metadata failure")
	}
	return physicalHash, true, nil
}

type completeStorage struct{ deletes int }

func (*completeStorage) Driver() string { return "gcs" }
func (*completeStorage) Prepare(context.Context, Session) (PreparedTarget, error) {
	panic("unused")
}
func (*completeStorage) Write(context.Context, Session, io.Reader) (int64, error) {
	panic("unused")
}
func (*completeStorage) Stat(context.Context, string) (int64, error) { return 4, nil }
func (s *completeStorage) Delete(context.Context, string) error {
	s.deletes++
	return nil
}
func (*completeStorage) Cancel(context.Context, Session) error { panic("unused") }

func TestAlignedOverwriteCompleteRetriesMetadataWithoutDeletingFinalObject(t *testing.T) {
	key := "docs/report.txt"
	repository := &singleSessionRepository{session: Session{
		ID: "upload", LogicPath: "docs/report.txt", PhysicalHash: key, Size: 4,
		Status: StatusPending, ExpectedPhysicalHash: &key, ExpiresAt: time.Now().Add(time.Hour),
	}}
	publisher := &retryPublisher{}
	storage := &completeStorage{}
	service := New(repository, publisher, storage)

	if _, err := service.Complete(context.Background(), "upload"); err == nil || err.Error() != "transient metadata failure" {
		t.Fatalf("first Complete() error = %v", err)
	}
	completed, err := service.Complete(context.Background(), "upload")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusComplete || publisher.calls != 2 {
		t.Fatalf("completed session = %#v, metadata calls = %d", completed, publisher.calls)
	}
	if storage.deletes != 0 {
		t.Fatalf("final object Delete() calls = %d, want 0", storage.deletes)
	}
}
