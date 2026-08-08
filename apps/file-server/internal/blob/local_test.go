package blob

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLocalStoreNewRangeReader(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	writer, err := store.NewWriter(context.Background(), "alphabet.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		offset int64
		length int64
		want   string
	}{
		{name: "bounded", offset: 1, length: 3, want: "bcd"},
		{name: "to end", offset: 2, length: -1, want: "cdef"},
		{name: "empty", offset: 2, length: 0, want: ""},
		{name: "past end", offset: 20, length: 4, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader, err := store.NewRangeReader(context.Background(), "alphabet.txt", tt.offset, tt.length)
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			if got := string(content); got != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
		})
	}

	if _, err := store.NewRangeReader(context.Background(), "alphabet.txt", -1, 1); err == nil || !strings.Contains(err.Error(), "offset") {
		t.Fatalf("negative offset error = %v, want validation error", err)
	}
}

func TestLocalResumableUploadChunksOffsetCompleteAndCancel(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	committed, err := store.WriteUploadChunk(ctx, "resume-session", 0, strings.NewReader("abc"))
	if err != nil || committed != 3 {
		t.Fatalf("first chunk = %d, %v", committed, err)
	}
	offset, exists, err := store.UploadOffset(ctx, "resume-session")
	if err != nil || !exists || offset != 3 {
		t.Fatalf("offset = %d, exists=%t, err=%v", offset, exists, err)
	}
	committed, err = store.WriteUploadChunk(ctx, "resume-session", 2, strings.NewReader("wrong"))
	if !errors.Is(err, ErrUploadOffsetConflict) || committed != 3 {
		t.Fatalf("conflicting chunk = %d, %v", committed, err)
	}
	committed, err = store.WriteUploadChunk(ctx, "resume-session", 3, strings.NewReader("def"))
	if err != nil || committed != 6 {
		t.Fatalf("resumed chunk = %d, %v", committed, err)
	}
	if err := store.CompleteUpload(ctx, "resume-session", "docs/result.txt", 6, nil); err != nil {
		t.Fatal(err)
	}
	// A retry after the atomic rename must be idempotent.
	if err := store.CompleteUpload(ctx, "resume-session", "docs/result.txt", 6, nil); err != nil {
		t.Fatalf("idempotent complete: %v", err)
	}
	reader, err := store.NewReader(ctx, "docs/result.txt")
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(content) != "abcdef" {
		t.Fatalf("final content = %q, %v", content, err)
	}

	if _, err := store.WriteUploadChunk(ctx, "cancel-session", 0, strings.NewReader("partial")); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelUpload(ctx, "cancel-session"); err != nil {
		t.Fatal(err)
	}
	offset, exists, err = store.UploadOffset(ctx, "cancel-session")
	if err != nil || exists || offset != 0 {
		t.Fatalf("offset after cancel = %d, exists=%t, err=%v", offset, exists, err)
	}
}

func TestLocalStoreListHidesReservedMetadata(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"visible.txt":                   "data",
		"_vfs-link/metadata.json":       "{}",
		"_vfs-link/uploads/session.tmp": "pending",
	} {
		writer, err := store.NewWriter(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Name != "visible.txt" {
		t.Fatalf("List() = %#v, want only visible.txt", objects)
	}
}

func TestLocalStoreListIncludesFinalDotfiles(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := store.NewWriter(context.Background(), "docs/.env")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("safe=value")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	objects, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Name != "docs/.env" {
		t.Fatalf("List() = %#v, want final dotfile", objects)
	}
}
