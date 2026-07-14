package blob

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestGCSStoreCRUD(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/primary-objects/o"):
			if got := r.URL.Query().Get("name"); got != "report.txt" {
				t.Errorf("upload object name = %q, want report.txt", got)
			}
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Errorf("read upload body: %v", err)
			}
			fmt.Fprint(w, `{"bucket":"primary-objects","name":"report.txt","size":"4"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/primary-objects/report.txt":
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "data")
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/storage/v1/b/primary-objects/o"):
			fmt.Fprint(w, `{"items":[{"name":"report.txt","size":"4"}]}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/storage/v1/b/primary-objects/o/report.txt"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)

	store, err := NewGCS(context.Background(), "primary-objects")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	writer, err := store.NewWriter(context.Background(), "/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := store.NewReader(context.Background(), "/report.txt")
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("NewReader() error = %v, requests = %v", err, requests)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "data" {
		t.Fatalf("reader content = %q, want data", got)
	}

	objects, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Name != "report.txt" || objects[0].Size != 4 {
		t.Fatalf("List() = %#v", objects)
	}

	if err := store.Delete(context.Background(), "/report.txt"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("requests = %v, want upload/read/list/delete", requests)
	}
}
