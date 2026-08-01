package webdav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	xwebdav "golang.org/x/net/webdav"
)

func TestWebDAVFileLifecycle(t *testing.T) {
	objects, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	fs := NewFileSystem(store, objects)
	dav := &xwebdav.Handler{
		Prefix:     "/dav/",
		FileSystem: fs,
		LockSystem: xwebdav.NewMemLS(),
	}
	handler := secureRequests("/dav/", false, basicAuth("dav", "secret", transactionalWrites(conditionalRequests("/dav/", fs, dav))))

	request := func(method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://example.com"+target, bytes.NewReader(body))
		req.SetBasicAuth("dav", "secret")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	if response := request("MKCOL", "/dav/docs", nil, nil); response.Code != http.StatusCreated {
		t.Fatalf("MKCOL status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPut, "/dav/docs", []byte("not-a-collection"), nil); response.Code >= 200 && response.Code < 300 {
		t.Fatalf("PUT collection status = %d, want failure", response.Code)
	}
	putResponse := request(http.MethodPut, "/dav/docs/a.txt", []byte("hello"), nil)
	if putResponse.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, body = %s", putResponse.Code, putResponse.Body.String())
	}
	if record, found, err := store.Find(context.Background(), "docs/a.txt"); err != nil || !found || record.PhysicalHash != "docs/a.txt" {
		t.Fatalf("PUT physical mapping = %#v, found=%v err=%v", record, found, err)
	}
	response := request(http.MethodGet, "/dav/docs/a.txt", nil, map[string]string{"Range": "bytes=1-3"})
	if response.Code != http.StatusPartialContent || response.Body.String() != "ell" {
		t.Fatalf("range GET = (%d, %q), want (206, %q)", response.Code, response.Body.String(), "ell")
	}
	if putResponse.Header().Get("ETag") == "" || response.Header().Get("ETag") != putResponse.Header().Get("ETag") {
		t.Fatalf("ETag changed between PUT and GET: %q != %q", putResponse.Header().Get("ETag"), response.Header().Get("ETag"))
	}
	etag := putResponse.Header().Get("ETag")
	response = request(http.MethodPut, "/dav/docs/a.txt", []byte("wrong"), map[string]string{"If-Match": `"not-the-current-etag"`})
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("conditional PUT status = %d, want %d", response.Code, http.StatusPreconditionFailed)
	}
	response = request(http.MethodPut, "/dav/docs/a.txt", []byte("updated"), map[string]string{"If-Match": etag})
	if response.Code != http.StatusCreated {
		t.Fatalf("matching conditional PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	response = request(http.MethodPut, "/dav/docs/a.txt", []byte("must-not-overwrite"), map[string]string{"If-None-Match": "*"})
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match PUT status = %d, want %d", response.Code, http.StatusPreconditionFailed)
	}
	propPatch := []byte(`<?xml version="1.0" encoding="utf-8"?><D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><x:test xmlns:x="urn:test">value</x:test></D:prop></D:set></D:propertyupdate>`)
	response = request("PROPPATCH", "/dav/docs/a.txt", propPatch, map[string]string{"Content-Type": "application/xml"})
	if response.Code != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH status = %d, body = %s", response.Code, response.Body.String())
	}
	response = request(http.MethodGet, "/dav/docs/a.txt", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "updated" {
		t.Fatalf("GET after PROPPATCH = (%d, %q), want unchanged content", response.Code, response.Body.String())
	}
	response = request("PROPFIND", "/dav/docs/", nil, map[string]string{"Depth": "1"})
	if response.Code != http.StatusMultiStatus || !strings.Contains(response.Body.String(), "a.txt") {
		t.Fatalf("PROPFIND = (%d, %q), want entry a.txt", response.Code, response.Body.String())
	}
	response = request("MOVE", "/dav/docs/a.txt", nil, map[string]string{"Destination": "https://example.com/dav/docs/b.txt"})
	if response.Code != http.StatusCreated {
		t.Fatalf("MOVE status = %d, body = %s", response.Code, response.Body.String())
	}
	response = request("COPY", "/dav/docs/b.txt", nil, map[string]string{"Destination": "https://example.com/dav/docs/c.txt"})
	if response.Code != http.StatusCreated {
		t.Fatalf("COPY status = %d, body = %s", response.Code, response.Body.String())
	}
	if response = request(http.MethodDelete, "/dav/docs/b.txt", nil, nil); response.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, body = %s", response.Code, response.Body.String())
	}
	response = request(http.MethodGet, "/dav/docs/c.txt", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "updated" {
		t.Fatalf("copied file GET = (%d, %q), want (200, %q)", response.Code, response.Body.String(), "updated")
	}
}

func TestFailedPUTAndCOPYDoNotPublishObjects(t *testing.T) {
	objects, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	if err := store.UpsertDirectory(context.Background(), "docs"); err != nil {
		t.Fatal(err)
	}
	writeObject := func(logicPath, physicalHash, content string) {
		t.Helper()
		writer, err := objects.NewWriter(context.Background(), physicalHash)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, content); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReplaceFile(context.Background(), logicPath, physicalHash, int64(len(content))); err != nil {
			t.Fatal(err)
		}
	}
	writeObject("docs/existing.txt", "existing-object", "original")

	fs := NewFileSystem(store, objects)
	dav := &xwebdav.Handler{Prefix: "/dav/", FileSystem: fs, LockSystem: xwebdav.NewMemLS()}
	handler := secureRequests("/dav/", false, basicAuth("dav", "secret", transactionalWrites(conditionalRequests("/dav/", fs, dav))))
	request := func(method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://example.com"+target, body)
		req.SetBasicAuth("dav", "secret")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	response := request(http.MethodPut, "/dav/docs/existing.txt", &partialErrorReader{}, nil)
	if response.Code >= 200 && response.Code < 300 {
		t.Fatalf("failed PUT status = %d, want failure", response.Code)
	}
	response = request(http.MethodGet, "/dav/docs/existing.txt", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "original" {
		t.Fatalf("GET after failed PUT = (%d, %q), want original", response.Code, response.Body.String())
	}

	writeObject("docs/missing-source.txt", "missing-object", "source")
	if err := objects.Delete(context.Background(), "missing-object"); err != nil {
		t.Fatal(err)
	}
	response = request("COPY", "/dav/docs/missing-source.txt", nil, map[string]string{
		"Destination": "https://example.com/dav/docs/copied.txt",
	})
	if response.Code >= 200 && response.Code < 300 {
		t.Fatalf("failed COPY status = %d, want failure", response.Code)
	}
	if _, found, err := store.Find(context.Background(), "docs/copied.txt"); err != nil || found {
		t.Fatalf("failed COPY published destination: found=%v err=%v", found, err)
	}
}

type partialErrorReader struct {
	sent bool
}

func (r *partialErrorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("injected request body failure")
}

type memoryStore struct {
	mu      sync.Mutex
	records map[string]db.FileRecord
	nextID  int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{records: make(map[string]db.FileRecord), nextID: 1}
}

func (s *memoryStore) Find(_ context.Context, logicPath string) (db.FileRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[logicPath]
	return record, found, nil
}

func (s *memoryStore) ListDirectChildren(_ context.Context, dirPath string, _ db.DirectChildrenOptions) (db.DirectChildrenPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := withTrailingSlash(dirPath)
	var records []db.FileRecord
	for logicPath, record := range s.records {
		suffix := strings.TrimPrefix(logicPath, prefix)
		if suffix != logicPath && suffix != "" && !strings.Contains(suffix, "/") {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].LogicPath < records[j].LogicPath })
	return db.DirectChildrenPage{Records: records, Total: len(records)}, nil
}

func (s *memoryStore) ListPrefix(_ context.Context, prefix string) ([]db.FileRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []db.FileRecord
	for logicPath, record := range s.records {
		if strings.HasPrefix(logicPath, prefix) {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *memoryStore) UpsertDirectory(_ context.Context, logicPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[logicPath] = db.FileRecord{ID: s.nextID, LogicPath: logicPath, IsDirectory: true, UpdatedAt: time.Now()}
	s.nextID++
	return nil
}

func (s *memoryStore) ReplaceFile(_ context.Context, logicPath, physicalHash string, size int64) (string, error) {
	previous, _, err := s.ReplaceFileConditional(context.Background(), logicPath, physicalHash, size, nil, false)
	return previous, err
}

func (s *memoryStore) ReplaceFileConditional(
	_ context.Context,
	logicPath, physicalHash string,
	size int64,
	expectedPhysicalHash *string,
	requireAbsent bool,
) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.records[logicPath]
	if found && current.IsDirectory {
		return "", false, db.ErrIsDirectory
	}
	if requireAbsent && found {
		return "", false, nil
	}
	if expectedPhysicalHash != nil && (!found || current.PhysicalHash != *expectedPhysicalHash) {
		return "", false, nil
	}
	previous := current.PhysicalHash
	s.records[logicPath] = db.FileRecord{ID: s.nextID, LogicPath: logicPath, PhysicalHash: physicalHash, Size: size, UpdatedAt: time.Now()}
	s.nextID++
	return previous, true, nil
}

func (s *memoryStore) RenamePath(_ context.Context, fromPath, toPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if toPath == fromPath || strings.HasPrefix(toPath, withTrailingSlash(fromPath)) {
		return db.ErrInvalidMove
	}
	record, found := s.records[fromPath]
	if !found {
		return db.ErrNotFound
	}
	delete(s.records, fromPath)
	record.LogicPath = toPath
	s.records[toPath] = record
	oldPrefix, newPrefix := withTrailingSlash(fromPath), withTrailingSlash(toPath)
	for logicPath, child := range s.records {
		if strings.HasPrefix(logicPath, oldPrefix) {
			delete(s.records, logicPath)
			child.LogicPath = newPrefix + strings.TrimPrefix(logicPath, oldPrefix)
			s.records[child.LogicPath] = child
		}
	}
	return nil
}

func (s *memoryStore) DeletePath(_ context.Context, logicPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, logicPath)
	return nil
}

func (s *memoryStore) DeletePrefix(_ context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for logicPath := range s.records {
		if strings.HasPrefix(logicPath, prefix) {
			delete(s.records, logicPath)
		}
	}
	return nil
}

var _ metadataStore = (*memoryStore)(nil)
