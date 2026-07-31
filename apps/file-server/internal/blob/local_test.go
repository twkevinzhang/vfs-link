package blob

import (
	"context"
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
