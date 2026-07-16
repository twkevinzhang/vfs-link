package upload

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type unusedStorage struct{}

func (unusedStorage) Driver() string { return "test" }
func (unusedStorage) Prepare(context.Context, Session) (PreparedTarget, error) {
	panic("unexpected call")
}
func (unusedStorage) Write(context.Context, string, io.Reader) (int64, error) {
	panic("unexpected call")
}
func (unusedStorage) Stat(context.Context, string) (int64, error) { panic("unexpected call") }
func (unusedStorage) Delete(context.Context, string) error        { panic("unexpected call") }

func TestCreateRejectsFileOverMaxBytesBeforeAllocatingSession(t *testing.T) {
	service := New(nil, nil, unusedStorage{}, WithMaxBytes(10))
	_, err := service.Create(context.Background(), CreateInput{LogicPath: "/large.bin", Size: 11})
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
	store, err := db.NewJSONLocal(filepath.Join(root, "_vfs-link", "metadata.json"))
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

	session, err := service.Create(ctx, CreateInput{LogicPath: "/report.txt", Size: 4, ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if session.Driver != "local" || session.RequireAbsent != true {
		t.Fatalf("created session = %#v", session)
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
	record, found, err := store.Find(ctx, "/report.txt")
	if err != nil || !found || record.Size != 4 {
		t.Fatalf("file mapping = %#v, %t, %v", record, found, err)
	}
}

func TestLocalFolderUploadCreatesParentDirectories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewJSONLocal(filepath.Join(root, "_vfs-link", "metadata.json"))
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

	session, err := service.Create(ctx, CreateInput{LogicPath: "/photos/trip/day-1/image.txt", Size: 4, ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(ctx, session.ID, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}

	for _, logicPath := range []string{"/photos", "/photos/trip", "/photos/trip/day-1"} {
		record, found, err := store.Find(ctx, logicPath)
		if err != nil || !found || !record.IsDirectory {
			t.Fatalf("directory %q = %#v, %t, %v", logicPath, record, found, err)
		}
	}
	record, found, err := store.Find(ctx, "/photos/trip/day-1/image.txt")
	if err != nil || !found || record.IsDirectory || record.Size != 4 {
		t.Fatalf("file = %#v, %t, %v", record, found, err)
	}
}

func TestLocalUploadRejectsMoreThanDeclaredSize(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewJSONLocal(filepath.Join(root, "_vfs-link", "metadata.json"))
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
	session, err := service.Create(ctx, CreateInput{LogicPath: "/short.txt", Size: 4})
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
