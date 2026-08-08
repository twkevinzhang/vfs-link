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
	status, err := service.Find(ctx, session.ID)
	if err != nil || status.UploadedSize != 0 || status.Status == StatusUploaded {
		t.Fatalf("status after oversized body = %#v, %v", status, err)
	}
	if _, err := service.Complete(ctx, session.ID); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Complete() error = %v, want ErrInvalidSession", err)
	}
	if reader, err := objects.NewReader(ctx, "short.txt"); err == nil {
		_ = reader.Close()
		t.Fatal("oversized upload published final object")
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

	preflight, err := service.Preflight(ctx, []PreflightInput{{ClientID: "report", LogicPath: "report.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	overwrite, err := service.Create(ctx, CreateInput{LogicPath: "report.txt", Size: 4, Overwrite: true, TargetVersion: preflight[0].TargetVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, overwrite.ID, strings.NewReader("unsafe extra bytes")); err == nil {
		t.Fatal("oversized overwrite error = nil")
	}
	status, err := service.Find(ctx, overwrite.ID)
	if err != nil || status.UploadedSize != 0 || status.Status == StatusUploaded {
		t.Fatalf("overwrite status after oversized body = %#v, %v", status, err)
	}
	if _, err := service.Complete(ctx, overwrite.ID); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Complete() error = %v, want ErrInvalidSession", err)
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

func TestPreflightReportsAvailableConflictAndDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "existing.txt", "existing-object", 27); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "folder"); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithBlob(store, objects)

	results, err := service.Preflight(ctx, []PreflightInput{
		{ClientID: "new", LogicPath: "new.txt"},
		{ClientID: "old", LogicPath: "existing.txt"},
		{ClientID: "dir", LogicPath: "folder"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Status != PreflightAvailable || results[0].LogicPath != "new.txt" || results[0].Existing != nil || results[0].TargetVersion == "" {
		t.Fatalf("available result = %#v", results[0])
	}
	if results[1].Status != PreflightConflict || results[1].Existing == nil || results[1].Existing.Kind != "file" || results[1].Existing.Size != 27 || results[1].TargetVersion == "" {
		t.Fatalf("conflict result = %#v", results[1])
	}
	if results[2].Status != PreflightDirectory || results[2].Existing == nil || results[2].Existing.Kind != "directory" || results[2].TargetVersion == "" {
		t.Fatalf("directory result = %#v", results[2])
	}
}

type snapshotPublisher struct {
	file  File
	found bool
}

func (p *snapshotPublisher) FindFile(context.Context, string) (File, bool, error) {
	return p.file, p.found, nil
}
func (*snapshotPublisher) EnsureDirectory(context.Context, string) error { return nil }
func (*snapshotPublisher) ReplaceFile(context.Context, string, string, int64, *string, bool) (string, bool, error) {
	panic("unexpected call")
}

type prepareCountingStorage struct{ prepares int }

func (*prepareCountingStorage) Driver() string { return "test" }
func (s *prepareCountingStorage) Prepare(context.Context, Session) (PreparedTarget, error) {
	s.prepares++
	return PreparedTarget{}, nil
}
func (*prepareCountingStorage) Write(context.Context, Session, io.Reader) (int64, error) {
	panic("unexpected call")
}
func (*prepareCountingStorage) Stat(context.Context, string) (int64, error) { panic("unexpected call") }
func (*prepareCountingStorage) Delete(context.Context, string) error        { panic("unexpected call") }
func (*prepareCountingStorage) Cancel(context.Context, Session) error       { panic("unexpected call") }

func TestCreateRejectsChangedPreflightTargetBeforePreparingStorage(t *testing.T) {
	publisher := &snapshotPublisher{file: File{PhysicalHash: "object-v1", Size: 4, UpdatedAt: time.Unix(1, 0)}, found: true}
	storage := &prepareCountingStorage{}
	service := New(&singleSessionRepository{}, publisher, storage)
	results, err := service.Preflight(context.Background(), []PreflightInput{{ClientID: "one", LogicPath: "report.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	publisher.file = File{PhysicalHash: "object-v2", Size: 8, UpdatedAt: time.Unix(2, 0)}

	_, err = service.Create(context.Background(), CreateInput{
		LogicPath: "report.txt", Size: 4, Overwrite: true, TargetVersion: results[0].TargetVersion,
	})
	if !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("Create() error = %v, want ErrTargetChanged", err)
	}
	if storage.prepares != 0 {
		t.Fatalf("Prepare() calls = %d, want 0", storage.prepares)
	}
}

func TestCreateOverwriteRequiresTargetVersion(t *testing.T) {
	publisher := &snapshotPublisher{file: File{PhysicalHash: "object", Size: 4, UpdatedAt: time.Unix(1, 0)}, found: true}
	storage := &prepareCountingStorage{}
	service := New(&singleSessionRepository{}, publisher, storage)

	_, err := service.Create(context.Background(), CreateInput{LogicPath: "report.txt", Size: 4, Overwrite: true})
	if !errors.Is(err, ErrTargetVersionRequired) {
		t.Fatalf("Create() error = %v, want ErrTargetVersionRequired", err)
	}
	if storage.prepares != 0 {
		t.Fatalf("Prepare() calls = %d, want 0", storage.prepares)
	}
}

func TestOversizedResumedChunkRollsBackOnlyCurrentChunk(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
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
	session, err := service.Create(ctx, CreateInput{LogicPath: "resume-oversized.bin", Size: 6})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.WriteChunk(ctx, session.ID, 0, 2, 6, strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	failed, err := service.WriteChunk(ctx, session.ID, 3, 5, 6, strings.NewReader("def-extra"))
	if err == nil || failed.UploadedSize != 3 {
		t.Fatalf("oversized resumed chunk = %#v, %v", failed, err)
	}
	status, err := service.Find(ctx, session.ID)
	if err != nil || status.UploadedSize != 3 || status.Status == StatusUploaded {
		t.Fatalf("status after rollback = %#v, %v", status, err)
	}
	if _, err := service.Complete(ctx, session.ID); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Complete() error = %v, want ErrInvalidSession", err)
	}
	resumed, err := service.WriteChunk(ctx, session.ID, 3, 5, 6, strings.NewReader("def"))
	if err != nil || resumed.Status != StatusUploaded {
		t.Fatalf("resume after rollback = %#v, %v", resumed, err)
	}
}

func TestExpiredSessionReportsStatusAndRejectsWriteOrComplete(t *testing.T) {
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
	service := NewWithBlob(store, objects, WithTTL(time.Minute))
	createdAt := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return createdAt }
	session, err := service.Create(ctx, CreateInput{LogicPath: "expired.txt", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return createdAt.Add(2 * time.Minute) }

	expired, err := service.Find(ctx, session.ID)
	if err != nil || expired.Status != StatusExpired || expired.Error != ErrExpired.Error() {
		t.Fatalf("expired session = %#v, %v", expired, err)
	}
	if _, err := service.WriteChunk(ctx, session.ID, 0, 3, 4, strings.NewReader("data")); !errors.Is(err, ErrExpired) {
		t.Fatalf("WriteChunk() error = %v, want ErrExpired", err)
	}
	if _, err := service.Complete(ctx, session.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("Complete() error = %v, want ErrExpired", err)
	}
}

type chunkReadError struct {
	content string
	read    bool
}

func (r *chunkReadError) Read(destination []byte) (int, error) {
	if r.read {
		return 0, errors.New("connection interrupted")
	}
	r.read = true
	return copy(destination, r.content), nil
}

func TestInterruptedLocalChunkKeepsCommittedOffsetForResume(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
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
	session, err := service.Create(ctx, CreateInput{LogicPath: "resume.bin", Size: 6})
	if err != nil {
		t.Fatal(err)
	}

	partial, err := service.WriteChunk(ctx, session.ID, 0, 5, 6, &chunkReadError{content: "ab"})
	if err == nil || partial.UploadedSize != 2 {
		t.Fatalf("interrupted chunk = %#v, %v", partial, err)
	}
	status, err := service.Find(ctx, session.ID)
	if err != nil || status.UploadedSize != 2 {
		t.Fatalf("status after interruption = %#v, %v", status, err)
	}
	resumed, err := service.WriteChunk(ctx, session.ID, 2, 5, 6, strings.NewReader("cdef"))
	if err != nil || resumed.Status != StatusUploaded || resumed.UploadedSize != 6 {
		t.Fatalf("resumed chunk = %#v, %v", resumed, err)
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
func (*completeStorage) WriteChunk(context.Context, Session, int64, io.Reader) (int64, error) {
	panic("unused")
}
func (*completeStorage) RollbackChunk(context.Context, Session, int64) error { panic("unused") }
func (*completeStorage) Offset(_ context.Context, session Session) (int64, bool, error) {
	return session.UploadedSize, session.UploadedSize == session.Size, nil
}
func (*completeStorage) Finalize(context.Context, Session) (int64, error) { return 4, nil }

func TestAlignedOverwriteCompleteRetriesMetadataWithoutDeletingFinalObject(t *testing.T) {
	key := "docs/report.txt"
	repository := &singleSessionRepository{session: Session{
		ID: "upload", LogicPath: "docs/report.txt", PhysicalHash: key, Size: 4,
		Status: StatusUploaded, UploadedSize: 4, ExpectedPhysicalHash: &key, ExpiresAt: time.Now().Add(time.Hour),
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
