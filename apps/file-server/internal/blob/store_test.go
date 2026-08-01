package blob

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNewStoreLocal(t *testing.T) {
	t.Parallel()

	store, err := NewStore(context.Background(), StoreConfig{
		Driver:    DriverLocal,
		LocalRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if store.Driver() != DriverLocal {
		t.Fatalf("Driver() = %q, want %q", store.Driver(), DriverLocal)
	}
}

type generationStoreStub struct {
	generation int64
	statCalls  int
	match      int64
}

func (*generationStoreStub) Close() error   { return nil }
func (*generationStoreStub) Driver() string { return DriverGCS }
func (*generationStoreStub) Root() string   { return "gs://test" }
func (*generationStoreStub) NewReader(context.Context, string) (io.ReadCloser, error) {
	panic("unused")
}
func (*generationStoreStub) NewRangeReader(context.Context, string, int64, int64) (io.ReadCloser, error) {
	panic("unused")
}
func (*generationStoreStub) NewWriter(context.Context, string) (io.WriteCloser, error) {
	panic("unused")
}
func (*generationStoreStub) Delete(context.Context, string) error       { return nil }
func (*generationStoreStub) List(context.Context) ([]ObjectInfo, error) { panic("unused") }
func (s *generationStoreStub) NewWriterIfGenerationMatch(_ context.Context, _ string, generation int64) (io.WriteCloser, error) {
	s.match = generation
	return discardWriteCloser{Writer: io.Discard}, nil
}
func (s *generationStoreStub) StatObject(context.Context, string) (ObjectInfo, error) {
	s.statCalls++
	return ObjectInfo{Generation: s.generation}, nil
}

type discardWriteCloser struct{ io.Writer }

func (discardWriteCloser) Close() error { return nil }

func TestNewStoreGCS(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "http://127.0.0.1:4443")

	store, err := NewStore(context.Background(), StoreConfig{
		Driver:    DriverGCS,
		GCSBucket: "primary-objects",
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if store.Driver() != DriverGCS {
		t.Fatalf("Driver() = %q, want %q", store.Driver(), DriverGCS)
	}
	if store.Root() != "gs://primary-objects" {
		t.Fatalf("Root() = %q, want gs://primary-objects", store.Root())
	}
}

func TestNewStoreRejectsUnsupportedDriver(t *testing.T) {
	t.Parallel()

	_, err := NewStore(context.Background(), StoreConfig{Driver: "s3"})
	if err == nil || !strings.Contains(err.Error(), `unsupported storage driver "s3"`) {
		t.Fatalf("NewStore() error = %v, want unsupported driver error", err)
	}
}

func TestIsReservedObjectProtectsAllVFSLinkPrefixes(t *testing.T) {
	for _, name := range []string{"_vfs-link/index.json", "_vfs-link-v2/tree/node", "_VFS-LINK-future/value", "/_vfs-link"} {
		if !isReservedObject(name) {
			t.Errorf("isReservedObject(%q) = false", name)
		}
	}
	for _, name := range []string{"docs/_vfs-link/file", "_vfs/file", " _vfs-link/file"} {
		if isReservedObject(name) {
			t.Errorf("isReservedObject(%q) = true", name)
		}
	}
}

func TestNewUploadWriterUsesCurrentGenerationForAlignedOverwrite(t *testing.T) {
	store := &generationStoreStub{generation: 77}
	key := "docs/report.txt"
	writer, err := NewUploadWriter(context.Background(), store, key, &key)
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if store.match != 77 || store.statCalls != 1 {
		t.Fatalf("generation match = %d, stat calls = %d", store.match, store.statCalls)
	}
}

func TestNewUploadWriterUsesDoesNotExistForNewFinalKey(t *testing.T) {
	store := &generationStoreStub{generation: 77}
	writer, err := NewUploadWriter(context.Background(), store, "docs/report.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if store.match != 0 || store.statCalls != 0 {
		t.Fatalf("generation match = %d, stat calls = %d", store.match, store.statCalls)
	}
}

func TestLocalNewUploadWriterRejectsSanitizerCollision(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing, err := store.NewWriter(context.Background(), "docs/A_B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := existing.Write([]byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := existing.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewUploadWriter(context.Background(), store, "docs/A_B.txt", nil); !errors.Is(err, ErrObjectCollision) {
		t.Fatalf("NewUploadWriter() error = %v, want ErrObjectCollision", err)
	}
}

func TestLocalAbortableOverwritePreservesExistingFinalObject(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "docs/report.txt"
	existing, err := store.NewWriter(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = existing.Write([]byte("original"))
	if err := existing.Close(); err != nil {
		t.Fatal(err)
	}

	writer, err := NewUploadWriter(context.Background(), store, key, &key)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte("partial"))
	abortable, ok := writer.(AbortableWriter)
	if !ok {
		t.Fatal("local writer is not abortable")
	}
	if err := abortable.CloseWithError(errors.New("transfer failed")); err != nil {
		t.Fatal(err)
	}
	reader, err := store.NewReader(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("final content = %q, want original", content)
	}
}
