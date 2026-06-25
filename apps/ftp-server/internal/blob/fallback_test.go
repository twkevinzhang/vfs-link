package blob

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

type memoryStore struct {
	objects map[string]string
}

func (s *memoryStore) Close() error {
	return nil
}

func (s *memoryStore) Driver() string {
	return "memory"
}

func (s *memoryStore) Root() string {
	return "memory://objects"
}

func (s *memoryStore) NewReader(_ context.Context, physicalHash string) (io.ReadCloser, error) {
	value, ok := s.objects[physicalHash]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(value)), nil
}

func (s *memoryStore) NewWriter(_ context.Context, physicalHash string) (io.WriteCloser, error) {
	return nil, os.ErrPermission
}

func (s *memoryStore) Delete(_ context.Context, physicalHash string) error {
	delete(s.objects, physicalHash)
	return nil
}

func (s *memoryStore) List(_ context.Context) ([]ObjectInfo, error) {
	return nil, nil
}

func TestFallbackStoreBackfillsPrimary(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}
	fallback := &memoryStore{objects: map[string]string{"legacy.mp4": "legacy-data"}}
	store := NewFallback(local, fallback, nil)
	defer store.Close()

	reader, err := store.NewReader(ctx, "legacy.mp4")
	if err != nil {
		t.Fatalf("NewReader() error = %v", err)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if string(content) != "legacy-data" {
		t.Fatalf("content = %q, want legacy-data", content)
	}

	delete(fallback.objects, "legacy.mp4")
	reader, err = store.NewReader(ctx, "legacy.mp4")
	if err != nil {
		t.Fatalf("second NewReader() error = %v", err)
	}
	content, err = io.ReadAll(reader)
	if err != nil {
		t.Fatalf("second ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if string(content) != "legacy-data" {
		t.Fatalf("second content = %q, want legacy-data", content)
	}
}
