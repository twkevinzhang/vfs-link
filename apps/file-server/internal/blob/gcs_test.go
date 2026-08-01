package blob

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNewGCSEmulatorDoesNotRequireADC(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "http://127.0.0.1:4443")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing-credentials.json"))

	store, err := NewGCS(context.Background(), "primary-objects")
	if err != nil {
		t.Fatalf("NewGCS() error = %v, want emulator client without ADC", err)
	}
	t.Cleanup(func() { _ = store.Close() })
}

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
		case r.Method == http.MethodGet && r.URL.Path == "/primary-objects/report.txt" && r.Header.Get("Range") != "":
			if got := r.Header.Get("Range"); got != "bytes=1-2" {
				t.Errorf("Range header = %q, want bytes=1-2", got)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Range", "bytes 1-2/4")
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, "at")
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

	rangeReader, err := store.NewRangeReader(context.Background(), "/report.txt", 1, 2)
	if err != nil {
		t.Fatalf("NewRangeReader() error = %v", err)
	}
	rangeContent, err := io.ReadAll(rangeReader)
	if err != nil {
		t.Fatal(err)
	}
	if err := rangeReader.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(rangeContent); got != "at" {
		t.Fatalf("range reader content = %q, want at", got)
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
	if len(requests) != 5 {
		t.Fatalf("requests = %v, want upload/read/range/list/delete", requests)
	}
}

func TestGCSStoreCopyToGCSValidatesNames(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "http://127.0.0.1:1")
	store, err := NewGCS(context.Background(), "primary-objects")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CopyToGCS(context.Background(), "", "shares", "copy.txt", nil); err == nil {
		t.Fatal("CopyToGCS() error = nil, want validation error")
	}
}

func TestCleanObjectNamePreservesValidLeadingSpace(t *testing.T) {
	if got := cleanObjectName("/ docs/report.txt"); got != " docs/report.txt" {
		t.Fatalf("cleanObjectName() = %q", got)
	}
}

func TestGCSConditionalWriterEmitsGenerationPreconditions(t *testing.T) {
	var (
		mu      sync.Mutex
		matches []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/primary-objects/o") {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		mu.Lock()
		matches = append(matches, r.URL.Query().Get("ifGenerationMatch"))
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"bucket":"primary-objects","name":"report.txt","size":"4"}`)
	}))
	t.Cleanup(server.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
	store, err := NewGCS(context.Background(), "primary-objects")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, generation := range []int64{0, 42} {
		writer, err := store.NewWriterIfGenerationMatch(context.Background(), "report.txt", generation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte("data")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(matches) != 2 || matches[0] != "0" || matches[1] != "42" {
		t.Fatalf("ifGenerationMatch queries = %v, want [0 42]", matches)
	}
}

var _ GCSObjectCopier = (*GCSStore)(nil)
