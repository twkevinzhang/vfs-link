package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	if err := store.UpsertFile(ctx, "/a.txt", "object-a", 3); err != nil {
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
		Path: "/",
		Entries: []Entry{
			{
				Name: "/archive",
				Path: "/archive",
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
		LogicPath:   "/archive",
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
	for _, directory := range []string{"/archive", "/archive/nested"} {
		if err := store.UpsertDirectory(ctx, directory); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []struct {
		path string
		size int64
	}{
		{path: "/root.txt", size: 2},
		{path: "/archive/a.txt", size: 3},
		{path: "/archive/nested/b.txt", size: 5},
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
	requestJSON(t, handler, http.MethodGet, "/api/files?path=%2F&q=root", nil, http.StatusOK, &filtered)
	if filtered.FolderSummary != (FolderSummary{Files: 3, Directories: 2, Bytes: 10}) {
		t.Fatalf("filtered folderSummary = %#v", filtered.FolderSummary)
	}
	if filtered.VisibleBytes != 2 {
		t.Fatalf("filtered visibleBytes = %d, want 2", filtered.VisibleBytes)
	}

	var unfiltered FilesResponse
	requestJSON(t, handler, http.MethodGet, "/api/files?path=%2F", nil, http.StatusOK, &unfiltered)
	var archive *Entry
	for i := range unfiltered.Entries {
		if unfiltered.Entries[i].Path == "/archive" {
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
