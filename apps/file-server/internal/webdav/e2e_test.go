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
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
	xwebdav "golang.org/x/net/webdav"
)

type webDAVErrorAfterPublish struct {
	commandService
	err error
}

func (c webDAVErrorAfterPublish) PublishUploaded(ctx context.Context, intent fileops.PublishIntent) (fileops.PublishResult, error) {
	result, err := c.commandService.PublishUploaded(ctx, intent)
	if err != nil {
		return result, err
	}
	return result, c.err
}

func TestWebDAVPublicationResponseLossKeepsVisibleObject(t *testing.T) {
	ctx := context.Background()
	objects, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	base := fileops.New(store, objects, objects)
	commands := webDAVErrorAfterPublish{commandService: base, err: errors.New("publish response lost")}
	file, err := newUploadFile(ctx, store, commands, objects, "visible.txt")
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

func TestWebDAVFileLifecycle(t *testing.T) {
	objects, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	commands := newTestCommands(store)
	fs := NewFileSystemWithCommands(store, objects, commands)
	dav := &xwebdav.Handler{
		Prefix:     "/dav/",
		FileSystem: fs,
		LockSystem: xwebdav.NewMemLS(),
	}
	handler := secureRequests("/dav/", false, basicAuth("dav", "secret", transactionalWrites(conditionalRequests("/dav/", fs, rejectDirectoryCopies("/dav/", fs, dav)))))

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
	if record, found, err := store.Find(context.Background(), "docs/a.txt"); err != nil || !found || !objectkey.IsUploadGenerationForPath("docs/a.txt", record.PhysicalHash) {
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
	response = request("COPY", "/dav/docs", nil, map[string]string{"Destination": "https://example.com/dav/docs-copy"})
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("directory COPY status = %d, want %d", response.Code, http.StatusNotImplemented)
	}
	deletedRecord, found, err := store.Find(context.Background(), "docs/b.txt")
	if err != nil || !found {
		t.Fatalf("record before DELETE = %#v, found=%v err=%v", deletedRecord, found, err)
	}
	if response = request(http.MethodDelete, "/dav/docs/b.txt", nil, nil); response.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, body = %s", response.Code, response.Body.String())
	}
	if _, found, err := store.Find(context.Background(), "docs/b.txt"); err != nil || found {
		t.Fatalf("DELETE active mapping found=%v err=%v", found, err)
	}
	if trashed, found := commands.trashedRecord("docs/b.txt"); !found || trashed.PhysicalHash != deletedRecord.PhysicalHash {
		t.Fatalf("DELETE trash record = %#v, found=%v", trashed, found)
	}
	reader, err := objects.NewRangeReader(context.Background(), deletedRecord.PhysicalHash, 0, -1)
	if err != nil {
		t.Fatalf("DELETE removed restorable blob: %v", err)
	}
	deletedContent, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(deletedContent) != "updated" {
		t.Fatalf("restorable blob = %q, readErr=%v closeErr=%v", deletedContent, readErr, closeErr)
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

	commands := newTestCommands(store)
	fs := NewFileSystemWithCommands(store, objects, commands)
	dav := &xwebdav.Handler{Prefix: "/dav/", FileSystem: fs, LockSystem: xwebdav.NewMemLS()}
	handler := secureRequests("/dav/", false, basicAuth("dav", "secret", transactionalWrites(conditionalRequests("/dav/", fs, rejectDirectoryCopies("/dav/", fs, dav)))))
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

type testCommands struct {
	store   *memoryStore
	mu      sync.Mutex
	trashed map[string]db.FileRecord
}

func newTestCommands(store *memoryStore) *testCommands {
	return &testCommands{store: store, trashed: make(map[string]db.FileRecord)}
}

func (c *testCommands) CreateDirectory(ctx context.Context, logicPath string) (db.FileRecord, error) {
	if err := c.store.UpsertDirectory(ctx, logicPath); err != nil {
		return db.FileRecord{}, err
	}
	record, _, err := c.store.Find(ctx, logicPath)
	return record, err
}

func (c *testCommands) Relocate(ctx context.Context, source, target string) (fileops.MutationOutcome, error) {
	if err := c.store.RenamePath(ctx, source, target); err != nil {
		return fileops.MutationOutcome{}, err
	}
	record, found, err := c.store.Find(ctx, target)
	if err != nil {
		return fileops.MutationOutcome{}, err
	}
	if !found {
		return fileops.MutationOutcome{}, db.ErrNotFound
	}
	return fileops.MutationOutcome{Records: []db.FileRecord{record}}, nil
}

func (c *testCommands) DeleteToTrash(ctx context.Context, paths []string) (fileops.MutationOutcome, error) {
	var records []db.FileRecord
	for _, logicPath := range paths {
		record, found, err := c.store.Find(ctx, logicPath)
		if err != nil {
			return fileops.MutationOutcome{}, err
		}
		if !found {
			return fileops.MutationOutcome{}, db.ErrNotFound
		}
		records = append(records, record)
		if record.IsDirectory {
			children, err := c.store.ListPrefix(ctx, withTrailingSlash(logicPath))
			if err != nil {
				return fileops.MutationOutcome{}, err
			}
			records = append(records, children...)
			if err := c.store.DeletePrefix(ctx, withTrailingSlash(logicPath)); err != nil {
				return fileops.MutationOutcome{}, err
			}
		}
		if err := c.store.DeletePath(ctx, logicPath); err != nil {
			return fileops.MutationOutcome{}, err
		}
	}
	c.mu.Lock()
	for _, record := range records {
		c.trashed[record.LogicPath] = record
	}
	c.mu.Unlock()
	return fileops.MutationOutcome{Records: records}, nil
}

func (c *testCommands) PublishUploaded(ctx context.Context, intent fileops.PublishIntent) (fileops.PublishResult, error) {
	previous, matched, err := c.store.ReplaceFileConditional(
		ctx, intent.LogicPath, intent.PhysicalHash, intent.Size,
		intent.ExpectedPhysicalHash, intent.RequireAbsent,
	)
	if err != nil {
		return fileops.PublishResult{}, err
	}
	if !matched {
		return fileops.PublishResult{}, db.ErrPathConflict
	}
	record, found, err := c.store.Find(ctx, intent.LogicPath)
	if err != nil {
		return fileops.PublishResult{}, err
	}
	if !found {
		return fileops.PublishResult{}, db.ErrNotFound
	}
	return fileops.PublishResult{Published: record, PreviousObject: previous}, nil
}

func (*testCommands) WaitVisible(context.Context, string) (db.OperationRecord, error) {
	return db.OperationRecord{}, nil
}

func (c *testCommands) trashedRecord(logicPath string) (db.FileRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, found := c.trashed[logicPath]
	return record, found
}

var _ commandService = (*testCommands)(nil)
