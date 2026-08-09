package upload

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

func testUploadObjectKey(t *testing.T, logicPath, uploadID string) string {
	t.Helper()
	key, err := objectkey.ForUpload(logicPath, uploadID)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

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
	if !strings.HasPrefix(session.PhysicalHash, "report.txt.__vfs_upload_") {
		t.Fatalf("physical hash = %q, want immutable upload key", session.PhysicalHash)
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
	reader, err := objects.NewReader(ctx, first.PhysicalHash)
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
func (*snapshotPublisher) ReplaceFile(context.Context, string, string, int64, *string, *FileSnapshot, bool) (PublishResult, bool, error) {
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

func TestCreatePersistsExactTargetSnapshotForCompletionCAS(t *testing.T) {
	updatedAt := time.Unix(123, 456).UTC()
	publisher := &snapshotPublisher{file: File{
		ID: 42, PhysicalHash: "same-object-key", Size: 4, UpdatedAt: updatedAt,
	}, found: true}
	repository := &singleSessionRepository{}
	service := New(repository, publisher, &prepareCountingStorage{})
	preflight, err := service.Preflight(context.Background(), []PreflightInput{{ClientID: "one", LogicPath: "report.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Create(context.Background(), CreateInput{
		LogicPath: "report.txt", Size: 4, Overwrite: true, TargetVersion: preflight[0].TargetVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ExpectedFileID != 42 || session.ExpectedFileUpdatedAt == nil || !session.ExpectedFileUpdatedAt.Equal(updatedAt) {
		t.Fatalf("expected target snapshot = id %d at %v", session.ExpectedFileID, session.ExpectedFileUpdatedAt)
	}
	if session.ExpectedPhysicalHash == nil || *session.ExpectedPhysicalHash != "same-object-key" {
		t.Fatalf("expected physical hash = %v", session.ExpectedPhysicalHash)
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

type singleSessionRepository struct {
	mu      sync.Mutex
	session Session
}

func (r *singleSessionRepository) CreateUpload(_ context.Context, session Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session.Revision == 0 {
		session.Revision = 1
	}
	r.session = session
	return nil
}
func (r *singleSessionRepository) FindUpload(context.Context, string) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session, true, nil
}
func (r *singleSessionRepository) ListDueRecoveries(_ context.Context, now time.Time, limit int) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		return []Session{}, nil
	}
	session := r.session
	completionDue := (session.Status == StatusUploaded || session.Status == StatusFinalizing) &&
		(session.CompletionStatus == CompletionPending || session.CompletionStatus == CompletionObjectReady || session.CompletionStatus == CompletionPublished) &&
		(session.CompletionNextAttemptAt == nil || !session.CompletionNextAttemptAt.After(now)) &&
		(session.CompletionLeaseUntil == nil || !session.CompletionLeaseUntil.After(now))
	cleanupDue := session.Status == StatusComplete && session.CompletionStatus == CompletionComplete && session.CleanupStatus == CleanupPending
	if completionDue || cleanupDue {
		return []Session{session}, nil
	}
	return []Session{}, nil
}
func (r *singleSessionRepository) UpdateUpload(_ context.Context, session Session, expected int64) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.Revision != expected {
		return r.session, false, nil
	}
	session.Revision = expected + 1
	r.session = session
	return session, true, nil
}
func (r *singleSessionRepository) RequestCompletion(_ context.Context, _ string, now time.Time) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if terminal, _ := completionTerminal(r.session); terminal || r.session.Status == StatusCancelling {
		return r.session, false, nil
	}
	if r.session.Status == StatusUploaded {
		r.session.Status = StatusFinalizing
		if r.session.CompletionStatus == "" {
			r.session.CompletionStatus = CompletionPending
		}
		r.session.UpdatedAt = now
		r.session.Revision++
	}
	return r.session, true, nil
}
func (r *singleSessionRepository) ClaimCompletion(_ context.Context, _, owner string, now, until time.Time) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.Status != StatusFinalizing || (r.session.CompletionNextAttemptAt != nil && r.session.CompletionNextAttemptAt.After(now)) {
		return r.session, false, nil
	}
	if r.session.CompletionOwner != "" && r.session.CompletionLeaseUntil != nil && r.session.CompletionLeaseUntil.After(now) {
		return r.session, false, nil
	}
	r.session.CompletionOwner = owner
	r.session.CompletionLeaseUntil = &until
	r.session.CompletionAttempts++
	r.session.Revision++
	return r.session, true, nil
}
func (r *singleSessionRepository) MarkObjectReady(_ context.Context, _, owner string, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.CompletionOwner != owner {
		return r.session, errors.New("stale owner")
	}
	r.session.CompletionStatus = CompletionObjectReady
	r.session.FinalizedAt = &now
	r.session.Revision++
	return r.session, nil
}
func (r *singleSessionRepository) MarkPublished(_ context.Context, _, owner, previous, cleanupStatus, cleanupError string, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.CompletionOwner != owner {
		return r.session, errors.New("stale owner")
	}
	r.session.CompletionStatus = CompletionPublished
	r.session.PublishedAt = &now
	r.session.PreviousPhysicalHash = previous
	r.session.CleanupStatus = cleanupStatus
	r.session.CleanupError = cleanupError
	r.session.Revision++
	return r.session, nil
}
func (r *singleSessionRepository) MarkComplete(_ context.Context, _, owner string, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.CompletionOwner != owner {
		return r.session, errors.New("stale owner")
	}
	r.session.Status = StatusComplete
	r.session.CompletionStatus = CompletionComplete
	r.session.CompletedAt = &now
	r.session.CompletionOwner = ""
	r.session.CompletionLeaseUntil = nil
	r.session.Error = ""
	r.session.Revision++
	return r.session, nil
}
func (r *singleSessionRepository) RetryCompletion(_ context.Context, _, owner, message string, next, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.CompletionOwner != owner {
		return r.session, errors.New("stale owner")
	}
	r.session.CompletionOwner = ""
	r.session.CompletionLeaseUntil = nil
	r.session.CompletionNextAttemptAt = &next
	r.session.LastCompletionError = message
	r.session.Error = message
	r.session.UpdatedAt = now
	r.session.Revision++
	return r.session, nil
}
func (r *singleSessionRepository) MarkCompletionConflict(_ context.Context, _, owner, message string, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.CompletionOwner != owner {
		return r.session, errors.New("stale owner")
	}
	r.session.Status = StatusConflict
	r.session.CompletionStatus = CompletionConflict
	r.session.Error = message
	r.session.LastCompletionError = message
	r.session.CompletionOwner = ""
	r.session.CompletionLeaseUntil = nil
	r.session.UpdatedAt = now
	r.session.Revision++
	return r.session, nil
}
func (r *singleSessionRepository) RequestCancel(_ context.Context, _ string, now time.Time) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.Status == StatusFinalizing || r.session.Status == StatusComplete || r.session.Status == StatusCancelled || r.session.Status == StatusConflict {
		return r.session, false, nil
	}
	if r.session.Status != StatusCancelling {
		r.session.Status = StatusCancelling
		r.session.CancelRequestedAt = &now
		r.session.Revision++
	}
	return r.session, true, nil
}
func (r *singleSessionRepository) MarkCancelled(_ context.Context, _ string, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.session.Status = StatusCancelled
	r.session.CancelledAt = &now
	r.session.Error = ""
	r.session.Revision++
	return r.session, nil
}
func (r *singleSessionRepository) ExpireUpload(_ context.Context, _ string, expected int64, now time.Time) (Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.Revision != expected || !uploadCanExpire(r.session.Status) {
		return r.session, false, nil
	}
	r.session.Status = StatusExpired
	r.session.Error = ErrExpired.Error()
	r.session.UpdatedAt = now
	r.session.Revision++
	return r.session, true, nil
}
func (r *singleSessionRepository) MarkCleanupComplete(_ context.Context, _ string, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.Status != StatusComplete || r.session.CleanupStatus != CleanupPending {
		return r.session, errors.New("cleanup transition unavailable")
	}
	r.session.CleanupStatus = CleanupComplete
	r.session.CleanupError = ""
	r.session.UpdatedAt = now
	r.session.Revision++
	return r.session, nil
}
func (r *singleSessionRepository) RetryCleanup(_ context.Context, _ string, message string, now time.Time) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session.Status != StatusComplete || r.session.CleanupStatus != CleanupPending {
		return r.session, errors.New("cleanup transition unavailable")
	}
	r.session.CleanupError = message
	r.session.UpdatedAt = now
	r.session.Revision++
	return r.session, nil
}

type retryPublisher struct{ calls int }

func (*retryPublisher) FindFile(context.Context, string) (File, bool, error) { panic("unused") }
func (*retryPublisher) EnsureDirectory(context.Context, string) error        { return nil }
func (p *retryPublisher) ReplaceFile(_ context.Context, _, physicalHash string, _ int64, _ *string, _ *FileSnapshot, _ bool) (PublishResult, bool, error) {
	p.calls++
	if p.calls == 1 {
		return PublishResult{}, false, errors.New("transient metadata failure")
	}
	return PublishResult{PreviousPhysicalHash: physicalHash}, true, nil
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
	key := testUploadObjectKey(t, "docs/report.txt", "upload")
	expected := "docs/report.txt"
	repository := &singleSessionRepository{session: Session{
		ID: "upload", LogicPath: "docs/report.txt", PhysicalHash: key, Size: 4,
		Status: StatusUploaded, UploadedSize: 4, ExpectedPhysicalHash: &expected, ExpiresAt: time.Now().Add(time.Hour),
	}}
	publisher := &retryPublisher{}
	storage := &completeStorage{}
	service := New(repository, publisher, storage)

	if _, err := service.Complete(context.Background(), "upload"); !errors.Is(err, ErrCompletionRetryable) || !strings.Contains(err.Error(), "transient metadata failure") {
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

type mappingPublisher struct {
	mu              sync.Mutex
	current         string
	calls           int
	entered         chan struct{}
	release         chan struct{}
	publish         PublishResult
	cleanup         CleanupResult
	cleanupErr      error
	cleanupFailures int
	cleanupCalls    int
	referenced      bool
	referenceErr    error
}

func (p *mappingPublisher) IsUploadGenerationReferenced(context.Context, string) (bool, error) {
	return p.referenced, p.referenceErr
}

func (p *mappingPublisher) RetryUploadCleanup(context.Context, CleanupIntent) (CleanupResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupCalls++
	if p.cleanupFailures > 0 {
		p.cleanupFailures--
		return CleanupResult{Pending: true, Error: "cleanup failed"}, errors.New("cleanup failed")
	}
	return p.cleanup, p.cleanupErr
}

func (*mappingPublisher) FindFile(context.Context, string) (File, bool, error) {
	panic("unused")
}
func (*mappingPublisher) EnsureDirectory(context.Context, string) error { return nil }
func (p *mappingPublisher) ReplaceFile(_ context.Context, _, physicalHash string, _ int64, expected *string, _ *FileSnapshot, absent bool) (PublishResult, bool, error) {
	p.mu.Lock()
	p.calls++
	if p.entered != nil && p.calls == 1 {
		close(p.entered)
		release := p.release
		p.mu.Unlock()
		<-release
		p.mu.Lock()
	}
	defer p.mu.Unlock()
	if p.current == physicalHash {
		return p.publish, true, nil
	}
	if (absent && p.current == "") || (expected != nil && p.current == *expected) {
		previous := p.current
		p.current = physicalHash
		result := p.publish
		if result.PreviousPhysicalHash == "" {
			result.PreviousPhysicalHash = previous
		}
		return result, true, nil
	}
	return PublishResult{}, false, nil
}

type failPublishedRepository struct {
	*singleSessionRepository
	failOnce bool
}

func (r *failPublishedRepository) MarkPublished(ctx context.Context, id, owner, previous, cleanupStatus, cleanupError string, now time.Time) (Session, error) {
	if r.failOnce {
		r.failOnce = false
		return Session{}, errors.New("lost metadata response")
	}
	return r.singleSessionRepository.MarkPublished(ctx, id, owner, previous, cleanupStatus, cleanupError, now)
}

type countingCompleteStorage struct {
	completeStorage
	mu        sync.Mutex
	finalizes int
}

func (s *countingCompleteStorage) Finalize(context.Context, Session) (int64, error) {
	s.mu.Lock()
	s.finalizes++
	s.mu.Unlock()
	return 4, nil
}

func TestCompleteRecoversAfterPublishSucceededBeforeCheckpoint(t *testing.T) {
	start := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	physicalHash := testUploadObjectKey(t, "docs/report.txt", "upload")
	repository := &failPublishedRepository{
		singleSessionRepository: &singleSessionRepository{session: Session{
			ID: "upload", LogicPath: "docs/report.txt", PhysicalHash: physicalHash, Size: 4,
			Status: StatusUploaded, UploadedSize: 4, RequireAbsent: true, Revision: 1, ExpiresAt: start.Add(time.Hour),
		}},
		failOnce: true,
	}
	publisher := &mappingPublisher{}
	storage := &countingCompleteStorage{}
	service := New(repository, publisher, storage)
	now := start
	service.now = func() time.Time { return now }

	if _, err := service.Complete(context.Background(), "upload"); !errors.Is(err, ErrCompletionRetryable) {
		t.Fatalf("first Complete() error = %v, want retryable", err)
	}
	if publisher.current != repository.session.PhysicalHash {
		t.Fatalf("published mapping = %q", publisher.current)
	}
	now = now.Add(defaultCompletionLease + time.Second)
	completed, err := service.Complete(context.Background(), "upload")
	if err != nil || completed.Status != StatusComplete {
		t.Fatalf("retried Complete() = %#v, %v", completed, err)
	}
	if storage.finalizes != 1 {
		t.Fatalf("Finalize() calls = %d, want 1", storage.finalizes)
	}
}

type lostCompleteResponseRepository struct {
	*singleSessionRepository
	failOnce bool
}

func (r *lostCompleteResponseRepository) MarkComplete(ctx context.Context, id, owner string, now time.Time) (Session, error) {
	completed, err := r.singleSessionRepository.MarkComplete(ctx, id, owner, now)
	if err != nil {
		return completed, err
	}
	if r.failOnce {
		r.failOnce = false
		return Session{}, errors.New("complete response lost")
	}
	return completed, nil
}

func TestCompleteRetryReturnsDurableSuccessAfterFinalCheckpointResponseLost(t *testing.T) {
	now := time.Now().UTC()
	physicalHash := testUploadObjectKey(t, "report.txt", "upload")
	repository := &lostCompleteResponseRepository{
		singleSessionRepository: &singleSessionRepository{session: Session{
			ID: "upload", LogicPath: "report.txt", PhysicalHash: physicalHash, Size: 4,
			Status: StatusUploaded, UploadedSize: 4, RequireAbsent: true, Revision: 1, ExpiresAt: now.Add(time.Hour),
		}},
		failOnce: true,
	}
	publisher := &mappingPublisher{}
	storage := &countingCompleteStorage{}
	service := New(repository, publisher, storage)

	if _, err := service.Complete(context.Background(), "upload"); !errors.Is(err, ErrCompletionRetryable) {
		t.Fatalf("first Complete() error = %v, want retryable", err)
	}
	completed, err := service.Complete(context.Background(), "upload")
	if err != nil || completed.Status != StatusComplete || completed.CompletedAt == nil {
		t.Fatalf("retried Complete() = %#v, %v", completed, err)
	}
	if storage.finalizes != 1 || publisher.calls != 1 {
		t.Fatalf("Finalize calls = %d, Publish calls = %d", storage.finalizes, publisher.calls)
	}
}

func TestConcurrentCompleteHasOneOwnerAndCancelCannotRegressIt(t *testing.T) {
	now := time.Now().UTC()
	physicalHash := testUploadObjectKey(t, "report.txt", "upload")
	repository := &singleSessionRepository{session: Session{
		ID: "upload", LogicPath: "report.txt", PhysicalHash: physicalHash, Size: 4,
		Status: StatusUploaded, UploadedSize: 4, RequireAbsent: true, Revision: 1, ExpiresAt: now.Add(time.Hour),
	}}
	publisher := &mappingPublisher{entered: make(chan struct{}), release: make(chan struct{})}
	service := New(repository, publisher, &countingCompleteStorage{})
	result := make(chan error, 1)
	go func() {
		_, err := service.Complete(context.Background(), "upload")
		result <- err
	}()
	<-publisher.entered

	const duplicateCallers = 19
	duplicates := make(chan error, duplicateCallers)
	for range duplicateCallers {
		go func() {
			_, err := service.Complete(context.Background(), "upload")
			duplicates <- err
		}()
	}
	for range duplicateCallers {
		if err := <-duplicates; !errors.Is(err, ErrCompletionInProgress) {
			t.Fatalf("concurrent Complete() error = %v", err)
		}
	}
	if err := service.Cancel(context.Background(), "upload"); !errors.Is(err, ErrCancellationUnavailable) {
		t.Fatalf("Cancel() error = %v", err)
	}
	close(publisher.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	status, err := service.Find(context.Background(), "upload")
	if err != nil || status.Status != StatusComplete {
		t.Fatalf("final status = %#v, %v", status, err)
	}
}

func TestCompleteRejectsLegacyMutableObjectKeyAsTerminalConflict(t *testing.T) {
	now := time.Now().UTC()
	repository := &singleSessionRepository{session: Session{
		ID: "legacy", LogicPath: "report.txt", PhysicalHash: "report.txt", Size: 4,
		Status: StatusUploaded, UploadedSize: 4, RequireAbsent: true, Revision: 1, ExpiresAt: now.Add(time.Hour),
	}}
	storage := &countingCompleteStorage{}
	service := New(repository, &mappingPublisher{}, storage)

	conflicted, err := service.Complete(context.Background(), "legacy")
	if !errors.Is(err, ErrLegacyObjectKey) {
		t.Fatalf("Complete() error = %v, want ErrLegacyObjectKey", err)
	}
	if conflicted.Status != StatusConflict || conflicted.CompletionStatus != CompletionConflict {
		t.Fatalf("Complete() = %#v, want terminal conflict", conflicted)
	}
	if storage.finalizes != 0 {
		t.Fatalf("Finalize() calls = %d, want 0", storage.finalizes)
	}
}

type offsetCountingStorage struct {
	completeStorage
	calls    int
	offset   int64
	complete bool
	err      error
}

func (s *offsetCountingStorage) Offset(context.Context, Session) (int64, bool, error) {
	s.calls++
	return s.offset, s.complete, s.err
}

func TestFindDoesNotReconcileTerminalOrFinalizingStates(t *testing.T) {
	for _, status := range []string{StatusFinalizing, StatusComplete, StatusCancelling, StatusCancelled, StatusConflict} {
		t.Run(status, func(t *testing.T) {
			repository := &singleSessionRepository{session: Session{
				ID: "upload", Status: status, Revision: 2, Size: 4, UploadedSize: 4, ExpiresAt: time.Now().Add(time.Hour),
			}}
			storage := &offsetCountingStorage{offset: 1}
			service := New(repository, &mappingPublisher{}, storage)
			found, err := service.Find(context.Background(), "upload")
			if err != nil || found.Status != status {
				t.Fatalf("Find() = %#v, %v", found, err)
			}
			if storage.calls != 0 {
				t.Fatalf("Offset() calls = %d, want 0", storage.calls)
			}
		})
	}
}

func TestFindReconcilesRemoteCompletionBeforeExpiry(t *testing.T) {
	now := time.Now().UTC()
	repository := &singleSessionRepository{session: Session{
		ID: "upload", Status: StatusUploading, Revision: 1, Size: 4, UploadedSize: 2, ExpiresAt: now.Add(-time.Second),
	}}
	storage := &offsetCountingStorage{offset: 4, complete: true}
	service := New(repository, &mappingPublisher{}, storage)
	service.now = func() time.Time { return now }

	status, err := service.Find(context.Background(), "upload")
	if err != nil || status.Status != StatusUploaded || status.UploadedSize != 4 {
		t.Fatalf("Find() = %#v, %v", status, err)
	}
}

func TestFindDoesNotExpireAlreadyUploadedSession(t *testing.T) {
	now := time.Now().UTC()
	repository := &singleSessionRepository{session: Session{
		ID: "upload", Status: StatusUploaded, Revision: 1, Size: 4, UploadedSize: 4, ExpiresAt: now.Add(-time.Second),
	}}
	service := New(repository, &mappingPublisher{}, &offsetCountingStorage{})
	service.now = func() time.Time { return now }

	status, err := service.Find(context.Background(), "upload")
	if err != nil || status.Status != StatusUploaded {
		t.Fatalf("Find() = %#v, %v", status, err)
	}
}

func TestFindExpiresPastDueSessionWhenResumableCapabilityIsGone(t *testing.T) {
	now := time.Now().UTC()
	repository := &singleSessionRepository{session: Session{
		ID: "upload", Status: StatusUploading, Revision: 1, Size: 4, UploadedSize: 2, ExpiresAt: now.Add(-time.Second),
	}}
	storage := &offsetCountingStorage{err: ErrResumableSessionGone}
	service := New(repository, &mappingPublisher{}, storage)
	service.now = func() time.Time { return now }

	found, err := service.Find(context.Background(), "upload")
	if err != nil || found.Status != StatusExpired || found.Error != ErrExpired.Error() {
		t.Fatalf("Find() = %#v, %v", found, err)
	}
}

func TestSamePathUploadsUseDifferentObjectsAndLoserCannotChangeWinner(t *testing.T) {
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
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithBlob(store, objects)
	first, err := service.Create(ctx, CreateInput{LogicPath: "same.txt", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(ctx, CreateInput{LogicPath: "same.txt", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	if first.PhysicalHash == second.PhysicalHash {
		t.Fatalf("uploads share physical key %q", first.PhysicalHash)
	}
	if _, err := service.Write(ctx, first.ID, strings.NewReader("safe")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, second.ID, strings.NewReader("evil")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(ctx, second.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("loser Complete() error = %v, want conflict", err)
	}
	record, found, err := store.Find(ctx, "same.txt")
	if err != nil || !found || record.PhysicalHash != first.PhysicalHash {
		t.Fatalf("winning mapping = %#v, %t, %v", record, found, err)
	}
	reader, err := objects.NewReader(ctx, record.PhysicalHash)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil || string(content) != "safe" {
		t.Fatalf("winning content = %q, %v", content, err)
	}
	if loser, openErr := objects.NewReader(ctx, second.PhysicalHash); openErr == nil {
		_ = loser.Close()
		t.Fatalf("losing immutable object %q was retained", second.PhysicalHash)
	}
}

func TestFormerWinnerConflictRetainsGenerationReferencedByShare(t *testing.T) {
	now := time.Now().UTC()
	physicalHash := testUploadObjectKey(t, "report.txt", "former-winner")
	repository := &singleSessionRepository{session: Session{
		ID: "former-winner", LogicPath: "report.txt", PhysicalHash: physicalHash,
		Size: 4, UploadedSize: 4, Status: StatusUploaded, RequireAbsent: true,
		Revision: 1, ExpiresAt: now.Add(time.Hour),
	}}
	publisher := &mappingPublisher{current: "newer-generation", referenced: true}
	storage := &completeStorage{}
	service := New(repository, publisher, storage)

	conflicted, err := service.Complete(context.Background(), "former-winner")
	if !errors.Is(err, ErrConflict) || conflicted.Status != StatusConflict {
		t.Fatalf("Complete() = %#v, %v", conflicted, err)
	}
	if storage.deletes != 0 {
		t.Fatalf("referenced former winner Delete() calls = %d, want 0", storage.deletes)
	}
}

func TestRetryCleanupDoesNotChangeBusinessCompletion(t *testing.T) {
	now := time.Now().UTC()
	repository := &singleSessionRepository{session: Session{
		ID: "upload", LogicPath: "report.txt", PhysicalHash: "new-object", PreviousPhysicalHash: "old-object",
		Status: StatusComplete, CompletionStatus: CompletionComplete, CleanupStatus: CleanupPending,
		CleanupError: "delete failed", Revision: 4, ExpiresAt: now.Add(time.Hour),
	}}
	publisher := &mappingPublisher{cleanup: CleanupResult{}}
	service := New(repository, publisher, &countingCompleteStorage{})

	cleaned, err := service.RetryCleanup(context.Background(), "upload")
	if err != nil || cleaned.Status != StatusComplete || cleaned.CleanupStatus != CleanupComplete || cleaned.CleanupError != "" {
		t.Fatalf("RetryCleanup() = %#v, %v", cleaned, err)
	}
}

func TestCompleteKeepsBusinessSuccessWhileCleanupRetriesOnReplay(t *testing.T) {
	now := time.Now().UTC()
	physicalHash := testUploadObjectKey(t, "report.txt", "upload")
	repository := &singleSessionRepository{session: Session{
		ID: "upload", LogicPath: "report.txt", PhysicalHash: physicalHash, Size: 4, UploadedSize: 4,
		Status: StatusUploaded, RequireAbsent: true, Revision: 1, ExpiresAt: now.Add(time.Hour),
	}}
	publisher := &mappingPublisher{
		publish:         PublishResult{PreviousPhysicalHash: "old-object", CleanupPending: true, CleanupError: "initial delete failed"},
		cleanupFailures: 1,
	}
	service := New(repository, publisher, &countingCompleteStorage{})

	completed, err := service.Complete(context.Background(), "upload")
	if err != nil || completed.Status != StatusComplete || completed.CleanupStatus != CleanupPending {
		t.Fatalf("first Complete() = %#v, %v", completed, err)
	}
	replayed, err := service.Complete(context.Background(), "upload")
	if err != nil || replayed.Status != StatusComplete || replayed.CleanupStatus != CleanupComplete {
		t.Fatalf("replayed Complete() = %#v, %v", replayed, err)
	}
	if publisher.cleanupCalls != 2 {
		t.Fatalf("cleanup calls = %d, want 2", publisher.cleanupCalls)
	}
}
