package upload

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type directStoreStub struct {
	statObject     blob.ObjectInfo
	statCalls      int
	startedObject  string
	startedMatch   int64
	cancelledURL   string
	deletedObject  string
	uploadedSize   int64
	uploadComplete bool
}

func (*directStoreStub) Close() error                                             { return nil }
func (*directStoreStub) Driver() string                                           { return blob.DriverGCS }
func (*directStoreStub) Root() string                                             { return "gs://test" }
func (*directStoreStub) NewReader(context.Context, string) (io.ReadCloser, error) { panic("unused") }
func (*directStoreStub) NewRangeReader(context.Context, string, int64, int64) (io.ReadCloser, error) {
	panic("unused")
}
func (*directStoreStub) NewWriter(context.Context, string) (io.WriteCloser, error) { panic("unused") }
func (s *directStoreStub) Delete(_ context.Context, object string) error {
	s.deletedObject = object
	return nil
}
func (*directStoreStub) List(context.Context) ([]blob.ObjectInfo, error) { panic("unused") }
func (s *directStoreStub) StartResumableUpload(_ context.Context, object, _, _ string, _, match int64) (string, map[string]string, error) {
	s.startedObject, s.startedMatch = object, match
	return "https://storage.example/session/opaque", nil, nil
}
func (s *directStoreStub) CancelResumableUpload(_ context.Context, sessionURL string) error {
	s.cancelledURL = sessionURL
	return nil
}
func (s *directStoreStub) QueryResumableUpload(context.Context, string, int64) (int64, bool, error) {
	return s.uploadedSize, s.uploadComplete, nil
}
func (s *directStoreStub) StatObject(context.Context, string) (blob.ObjectInfo, error) {
	s.statCalls++
	return s.statObject, nil
}

func TestGCSPrepareNewObjectUsesDoesNotExistPrecondition(t *testing.T) {
	objects := &directStoreStub{}
	storage := gcsDirectStorage{objects: objects}
	session := Session{PhysicalHash: "docs/report.txt", Size: 4, RequireAbsent: true}

	prepared, err := storage.Prepare(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if objects.startedObject != session.PhysicalHash || objects.startedMatch != 0 || objects.statCalls != 0 {
		t.Fatalf("prepare = object %q, match %d, stat calls %d", objects.startedObject, objects.startedMatch, objects.statCalls)
	}
	if prepared.URL == "" {
		t.Fatal("Prepare() URL is empty")
	}
}

func TestGCSPrepareOverwriteSnapshotsCurrentGeneration(t *testing.T) {
	objects := &directStoreStub{statObject: blob.ObjectInfo{Name: "docs/report.txt", Size: 4, Generation: 712}}
	storage := gcsDirectStorage{objects: objects}
	key := "docs/report.txt"
	session := Session{PhysicalHash: key, Size: 4, Overwrite: true, ExpectedPhysicalHash: &key}

	if _, err := storage.Prepare(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if objects.startedMatch != 712 || objects.statCalls != 1 {
		t.Fatalf("generation match = %d, stat calls = %d", objects.startedMatch, objects.statCalls)
	}
}

func TestGCSCancelInvalidatesSessionWithoutDeletingFinalObject(t *testing.T) {
	objects := &directStoreStub{}
	storage := gcsDirectStorage{objects: objects}
	session := Session{PhysicalHash: "docs/report.txt", UploadURL: "https://storage.example/session/opaque"}

	if err := storage.Cancel(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if objects.cancelledURL != session.UploadURL {
		t.Fatalf("cancelled URL = %q", objects.cancelledURL)
	}
	if objects.deletedObject != "" {
		t.Fatalf("final object was deleted: %q", objects.deletedObject)
	}
}

func TestGCSCancelWithoutPersistedSessionURLFailsSafe(t *testing.T) {
	objects := &directStoreStub{}
	err := (gcsDirectStorage{objects: objects}).Cancel(context.Background(), Session{PhysicalHash: "docs/report.txt"})
	if err != ErrCancellationUnavailable {
		t.Fatalf("Cancel() error = %v, want ErrCancellationUnavailable", err)
	}
	if objects.deletedObject != "" {
		t.Fatalf("final object was deleted: %q", objects.deletedObject)
	}
}

func TestGCSServiceCancelUsesPersistedSessionURL(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects := &directStoreStub{}
	service := NewWithBlob(metadata, objects)

	session, err := service.Create(ctx, CreateInput{LogicPath: "docs/report.txt", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if objects.cancelledURL != "https://storage.example/session/opaque" {
		t.Fatalf("cancelled URL = %q", objects.cancelledURL)
	}
	if objects.deletedObject != "" {
		t.Fatalf("final object was deleted: %q", objects.deletedObject)
	}
	if _, err := service.Find(ctx, session.ID); err != ErrNotFound {
		t.Fatalf("Find() after cancel error = %v, want ErrNotFound", err)
	}
}

func TestGCSServiceStatusRefreshesRemoteCommittedOffset(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects := &directStoreStub{uploadedSize: 3}
	service := NewWithBlob(metadata, objects)
	session, err := service.Create(ctx, CreateInput{LogicPath: "docs/report.txt", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Find(ctx, session.ID)
	if err != nil || status.UploadedSize != 3 || status.Status != StatusUploading {
		t.Fatalf("partial status = %#v, %v", status, err)
	}
	objects.uploadedSize, objects.uploadComplete = 4, true
	status, err = service.Find(ctx, session.ID)
	if err != nil || status.UploadedSize != 4 || status.Status != StatusUploaded {
		t.Fatalf("complete status = %#v, %v", status, err)
	}
}
