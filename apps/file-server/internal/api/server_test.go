package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type rejectListBlobStore struct{ blob.Store }

func (rejectListBlobStore) List(context.Context) ([]blob.ObjectInfo, error) {
	return nil, errors.New("physical object listing must not be used for status")
}

func TestStatsJSONUsesDriverNeutralObjectFields(t *testing.T) {
	payload, err := json.Marshal(Stats{
		ObjectCount: 2,
		ObjectBytes: 3,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	jsonText := string(payload)
	for _, field := range []string{`"objectCount":2`, `"objectBytes":3`} {
		if !strings.Contains(jsonText, field) {
			t.Errorf("Stats JSON = %s, want field %s", jsonText, field)
		}
	}
	for _, legacyField := range []string{"localObjectCount", "localObjectBytes"} {
		if strings.Contains(jsonText, legacyField) {
			t.Errorf("Stats JSON = %s, contains legacy field %q", jsonText, legacyField)
		}
	}
}

func TestStatusUsesMetadataStatsWithoutListingPhysicalBucket(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "a.txt", "object-a", 3); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}

	var status StatusResponse
	requestJSON(t, New(store, rejectListBlobStore{objects}, nil, "", "").Handler(), http.MethodGet, "/api/status", nil, http.StatusOK, &status)
	if status.Stats.FileCount != 1 || status.Stats.TotalBytes != 3 || status.Stats.ObjectCount != 1 || status.Stats.ObjectBytes != 3 {
		t.Fatalf("status stats = %#v", status.Stats)
	}
}

func TestFolderSummaryJSONKeepsRecursiveAndVisibleTotalsDistinct(t *testing.T) {
	response := FilesResponse{
		Path: "",
		Entries: []Entry{
			{
				Name: "archive",
				Path: "archive",
				Kind: "directory",
				FolderSummary: &FolderSummary{
					Files:       8,
					Directories: 2,
					Bytes:       4096,
				},
			},
		},
		FolderSummary: FolderSummary{
			Files:       10,
			Directories: 3,
			Bytes:       8192,
		},
		VisibleBytes: 128,
	}

	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded struct {
		FolderSummary FolderSummary `json:"folderSummary"`
		VisibleBytes  int64         `json:"visibleBytes"`
		Entries       []Entry       `json:"entries"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.FolderSummary.Bytes != 8192 || decoded.FolderSummary.Files != 10 {
		t.Fatalf("folderSummary = %#v", decoded.FolderSummary)
	}
	if decoded.VisibleBytes != 128 {
		t.Fatalf("visibleBytes = %d, want direct/query bytes 128", decoded.VisibleBytes)
	}
	if len(decoded.Entries) != 1 || decoded.Entries[0].FolderSummary == nil || decoded.Entries[0].FolderSummary.Bytes != 4096 {
		t.Fatalf("entries = %#v", decoded.Entries)
	}
}

func TestEntryFromRecordMapsDirectoryFolderSummary(t *testing.T) {
	record := db.FileRecord{
		LogicPath:   "archive",
		IsDirectory: true,
		FolderSummary: &db.FolderSummary{
			Files:       8,
			Directories: 2,
			Bytes:       4096,
		},
	}

	entry := entryFromRecord(record)
	if entry.FolderSummary == nil {
		t.Fatal("entry.FolderSummary = nil")
	}
	if *entry.FolderSummary != (FolderSummary{Files: 8, Directories: 2, Bytes: 4096}) {
		t.Fatalf("entry.FolderSummary = %#v", entry.FolderSummary)
	}
}

func TestFilesReturnsRecursiveFolderSummaryIndependentOfQuery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"archive", "archive/nested"} {
		if err := store.UpsertDirectory(ctx, directory); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []struct {
		path string
		size int64
	}{
		{path: "root.txt", size: 2},
		{path: "archive/a.txt", size: 3},
		{path: "archive/nested/b.txt", size: 5},
	} {
		if err := store.UpsertFile(ctx, file.path, "object"+file.path, file.size); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, objects, nil, "", "").Handler()

	var filtered FilesResponse
	requestJSON(t, handler, http.MethodGet, "/api/files?path=&q=root", nil, http.StatusOK, &filtered)
	if filtered.FolderSummary != (FolderSummary{Files: 3, Directories: 2, Bytes: 10}) {
		t.Fatalf("filtered folderSummary = %#v", filtered.FolderSummary)
	}
	if filtered.VisibleBytes != 2 {
		t.Fatalf("filtered visibleBytes = %d, want 2", filtered.VisibleBytes)
	}

	var unfiltered FilesResponse
	requestJSON(t, handler, http.MethodGet, "/api/files?path=", nil, http.StatusOK, &unfiltered)
	var archive *Entry
	for i := range unfiltered.Entries {
		if unfiltered.Entries[i].Path == "archive" {
			archive = &unfiltered.Entries[i]
			break
		}
	}
	if archive == nil || archive.FolderSummary == nil {
		t.Fatalf("archive entry summary missing: %#v", unfiltered.Entries)
	}
	if *archive.FolderSummary != (FolderSummary{Files: 2, Directories: 1, Bytes: 8}) {
		t.Fatalf("archive folderSummary = %#v", archive.FolderSummary)
	}
}

func TestFilesRejectsLegacyAbsoluteLogicalPath(t *testing.T) {
	store, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/files?path=%2Farchive", nil)
	New(store, objects, nil, "", "").Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "must not start with a slash") {
		t.Fatalf("legacy absolute path response = %d %s", response.Code, response.Body.String())
	}
}

func TestDownloadContentDispositionPreservesUnicodeFilenames(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store, objects, nil, "", "").Handler()

	for _, test := range []struct {
		name        string
		filename    string
		disposition string
	}{
		{name: "ASCII attachment", filename: "annual-report.pdf", disposition: "attachment"},
		{name: "Chinese attachment", filename: "俊男美女.rar", disposition: "attachment"},
		{name: "Japanese inline", filename: "日本語の資料.txt", disposition: "inline"},
		{name: "emoji attachment", filename: "photo 😀.jpg", disposition: "attachment"},
		{name: "kaomoji attachment", filename: "report (╯°□°)╯︵ ┻━┻.txt", disposition: "attachment"},
		{name: "quoted escaped percent attachment", filename: "quote\" slash\\ percent%.txt", disposition: "attachment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logicPath := "downloads/" + test.filename
			physicalHash := "objects/" + test.name
			if err := store.UpsertFile(ctx, logicPath, physicalHash, int64(len("download body"))); err != nil {
				t.Fatal(err)
			}
			writer, err := objects.NewWriter(ctx, physicalHash)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(writer, "download body"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			query := url.Values{"path": {logicPath}}
			if test.disposition == "inline" {
				query.Set("disposition", "inline")
			}
			request := httptest.NewRequest(http.MethodGet, "/api/download?"+query.Encode(), nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("download status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Body.String(); got != "download body" {
				t.Errorf("download body=%q, want %q", got, "download body")
			}

			header := recorder.Header().Get("Content-Disposition")
			if strings.ContainsAny(header, "\r\n") {
				t.Fatalf("Content-Disposition contains a raw line break: %q", header)
			}
			disposition, params, err := mime.ParseMediaType(header)
			if err != nil {
				t.Fatalf("mime.ParseMediaType(%q) error = %v", header, err)
			}
			if disposition != test.disposition {
				t.Errorf("disposition=%q, want %q", disposition, test.disposition)
			}
			if got := params["filename"]; got != test.filename {
				t.Errorf("filename=%q, want %q (header %q)", got, test.filename, header)
			}
		})
	}
}

func TestDownloadStreamsLargeFilesWithChunkedHTTP1Response(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}

	const downloadSize = 33 * 1024 * 1024
	payload := bytes.Repeat([]byte("vfs-link-large-download\n"), downloadSize/len("vfs-link-large-download\n")+1)[:downloadSize]
	const logicPath = "downloads/large-file.zip"
	const physicalHash = "large-download"
	if err := store.UpsertFile(ctx, logicPath, physicalHash, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	writer, err := objects.NewWriter(ctx, physicalHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(New(store, objects, nil, "", "").Handler())
	server.EnableHTTP2 = false
	server.Start()
	t.Cleanup(server.Close)

	response, err := server.Client().Get(server.URL + "/api/download?" + url.Values{"path": {logicPath}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.ProtoMajor != 1 {
		t.Fatalf("HTTP protocol = %s, want HTTP/1.x", response.Proto)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if response.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want -1 for chunked response", response.ContentLength)
	}
	if !containsString(response.TransferEncoding, "chunked") {
		t.Fatalf("TransferEncoding = %q, want chunked", response.TransferEncoding)
	}

	downloadedHash := sha256.New()
	downloadedSize, err := io.Copy(downloadedHash, response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if downloadedSize != int64(len(payload)) {
		t.Fatalf("downloaded bytes = %d, want %d", downloadedSize, len(payload))
	}
	expectedHash := sha256.Sum256(payload)
	if !bytes.Equal(downloadedHash.Sum(nil), expectedHash[:]) {
		t.Fatal("downloaded payload hash does not match source payload")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
