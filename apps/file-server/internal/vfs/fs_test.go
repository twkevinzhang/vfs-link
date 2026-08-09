package vfs

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
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

type errorAfterPublishCommands struct {
	FileCommands
	err   error
	after func(context.Context, fileops.PublishIntent) error
}

func (c errorAfterPublishCommands) PublishUploaded(ctx context.Context, intent fileops.PublishIntent) (fileops.PublishResult, error) {
	result, err := c.FileCommands.PublishUploaded(ctx, intent)
	if err != nil {
		return result, err
	}
	if c.after != nil {
		if afterErr := c.after(ctx, intent); afterErr != nil {
			return result, afterErr
		}
	}
	return result, c.err
}

func TestFSNameIsStorageDriverNeutral(t *testing.T) {
	fs := New(nil, nil)
	if got, want := fs.Name(), "vfs-link"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestMutationsUseSharedCommandsAndDeleteToTrash(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	fs := New(store, objects)
	if err := fs.Mkdir("/docs", 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := fs.Create("/docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = fs.Rename("/docs/a.txt", "/docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Find(ctx, "docs/b.txt")
	if err != nil || !found {
		t.Fatalf("renamed found=%t err=%v", found, err)
	}
	if err = fs.Remove("/docs/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, found, err = store.Find(ctx, "docs/b.txt"); err != nil || found {
		t.Fatalf("active mapping found=%t err=%v", found, err)
	}
	trash, err := store.ListTrash(ctx)
	if err != nil || len(trash) != 1 || trash[0].PhysicalHash != record.PhysicalHash {
		t.Fatalf("trash=%+v err=%v", trash, err)
	}
	if reader, err := objects.NewReader(ctx, record.PhysicalHash); err != nil {
		t.Fatalf("delete-to-trash removed object: %v", err)
	} else {
		_ = reader.Close()
	}
	if err = fs.Remove("/docs/missing.txt"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing remove error=%v", err)
	}
}

func TestCreatePublishesSanitizedFinalObjectKey(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	fs := New(store, objects)

	file, err := fs.Create("/docs/A:B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Find(ctx, "docs/A:B.txt")
	if err != nil || !found {
		t.Fatalf("Find() = found %t, error %v", found, err)
	}
	if !objectkey.IsUploadGenerationForPath("docs/A:B.txt", record.PhysicalHash) {
		t.Fatalf("PhysicalHash = %q, want immutable sanitized generation", record.PhysicalHash)
	}
}

func TestCreateIsolatesSanitizerCollisionsWithImmutableGenerations(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	fs := New(store, objects)
	first, err := fs.Create("/docs/A?B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	colliding, err := fs.Create("/docs/A:B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := colliding.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := colliding.Close(); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Find(ctx, "docs/A?B.txt")
	if err != nil || !found || !objectkey.IsUploadGenerationForPath("docs/A?B.txt", record.PhysicalHash) {
		t.Fatalf("original record = %#v, found %t, error %v", record, found, err)
	}
	collision, found, err := store.Find(ctx, "docs/A:B.txt")
	if err != nil || !found || !objectkey.IsUploadGenerationForPath("docs/A:B.txt", collision.PhysicalHash) || collision.PhysicalHash == record.PhysicalHash {
		t.Fatalf("colliding record = %#v, found %t, error %v", collision, found, err)
	}
}

func TestConcurrentProtocolWriterCannotOverwritePublishedGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	oldWriter, err := blob.NewUploadWriter(ctx, objects, "old-object", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(oldWriter, strings.NewReader("old!")); err != nil {
		t.Fatal(err)
	}
	if err = oldWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertFile(ctx, "same.txt", "old-object", 4); err != nil {
		t.Fatal(err)
	}

	fs := New(store, objects)
	loser, err := fs.Create("/same.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = loser.Write([]byte("ftp!")); err != nil {
		t.Fatal(err)
	}

	winnerKey, err := objectkey.ForUpload("same.txt", "http-winner")
	if err != nil {
		t.Fatal(err)
	}
	winnerWriter, err := blob.NewUploadWriter(ctx, objects, winnerKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(winnerWriter, strings.NewReader("http")); err != nil {
		t.Fatal(err)
	}
	if err = winnerWriter.Close(); err != nil {
		t.Fatal(err)
	}
	expected := "old-object"
	commands := fileops.New(store, objects, objects)
	if _, err = commands.PublishUploaded(ctx, fileops.PublishIntent{
		LogicPath: "same.txt", PhysicalHash: winnerKey, Size: 4, ExpectedPhysicalHash: &expected,
	}); err != nil {
		t.Fatal(err)
	}

	if err = loser.Close(); !errors.Is(err, db.ErrPathConflict) {
		t.Fatalf("losing protocol Close() error = %v, want path conflict", err)
	}
	record, found, err := store.Find(ctx, "same.txt")
	if err != nil || !found || record.PhysicalHash != winnerKey {
		t.Fatalf("winner mapping = %#v, found %t, err %v", record, found, err)
	}
	reader, err := objects.NewReader(ctx, winnerKey)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(content) != "http" {
		t.Fatalf("winner content = %q, err %v", content, err)
	}
	listed, err := objects.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].Name != winnerKey {
		t.Fatalf("objects after conflict = %#v, err %v", listed, err)
	}
}

func TestPublicationResponseLossDoesNotDeleteVisibleGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	base := fileops.New(store, objects, objects)
	commands := errorAfterPublishCommands{FileCommands: base, err: errors.New("publish response lost")}
	fs := NewWithCommands(store, objects, commands, time.Second)
	file, err := fs.Create("/visible.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("safe")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("Close() after committed response loss = %v", err)
	}
	record, found, err := store.Find(ctx, "visible.txt")
	if err != nil || !found {
		t.Fatalf("visible mapping = %#v, found %t, err %v", record, found, err)
	}
	reader, err := objects.NewReader(ctx, record.PhysicalHash)
	if err != nil {
		t.Fatalf("visible object was deleted: %v", err)
	}
	_ = reader.Close()
}

func TestFormerWinnerResponseLossRetainsShareReferencedGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	base := fileops.New(store, objects, objects)
	var formerWinner string
	commands := errorAfterPublishCommands{
		FileCommands: base,
		err:          errors.New("publish response lost"),
		after: func(ctx context.Context, intent fileops.PublishIntent) error {
			formerWinner = intent.PhysicalHash
			if _, err := store.CreateShareFromSnapshot(ctx, db.ShareRecord{
				ID: "share-former-winner", LogicPath: intent.LogicPath, PhysicalHash: formerWinner,
				FileName: "former.txt", Size: intent.Size, DestinationObject: "shares/former", ShareURL: "https://example.test/former", Status: "draft",
			}); err != nil {
				return err
			}
			newerKey, err := objectkey.ForUpload(intent.LogicPath, "newer-winner")
			if err != nil {
				return err
			}
			writer, err := blob.NewUploadWriter(ctx, objects, newerKey, nil)
			if err != nil {
				return err
			}
			if _, err = writer.Write([]byte("newer")); err != nil {
				return err
			}
			if err = writer.Close(); err != nil {
				return err
			}
			expected := formerWinner
			_, err = base.PublishUploaded(ctx, fileops.PublishIntent{
				LogicPath: intent.LogicPath, PhysicalHash: newerKey, Size: 5, ExpectedPhysicalHash: &expected,
			})
			return err
		},
	}
	fs := NewWithCommands(store, objects, commands, time.Second)
	file, err := fs.Create("/former.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("former")); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err == nil {
		t.Fatal("Close() error = nil, want ambiguous response error")
	}
	reader, err := objects.NewReader(ctx, formerWinner)
	if err != nil {
		t.Fatalf("Share-referenced former winner was deleted: %v", err)
	}
	_ = reader.Close()
	share, found, err := store.FindShare(ctx, "share-former-winner")
	if err != nil || !found || share.PhysicalHash != formerWinner {
		t.Fatalf("Share = %#v, found %t, err %v", share, found, err)
	}
}
