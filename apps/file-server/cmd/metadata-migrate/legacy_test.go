package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func TestDecodeAndValidateLegacy(t *testing.T) {
	snapshot, err := decodeLegacy(strings.NewReader(`{
  "version": 1,
  "nextFileId": 4,
  "files": [
    {"id":1,"logicPath":"/docs","physicalHash":"","size":0,"isDirectory":true},
    {"id":2,"logicPath":"/docs/a.txt","physicalHash":"objects/a","size":12,"isDirectory":false},
    {"id":3,"logicPath":"/old.txt","physicalHash":"objects/old","size":5,"isDirectory":false,"trashedAt":"2026-07-17T00:00:00Z","trashId":"trash-1","trashRoot":true}
  ],
  "shares": [{"id":"share-1"}],
  "davLocks": [],
  "uploads": []
}`))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := validateLegacy(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Files != 2 || summary.Directories != 1 || summary.Bytes != 17 || summary.MinID != 1 || summary.MaxID != 3 || summary.Shares != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestRunDryRunRejectsNonReservedTargetPrefix(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(filename, []byte(`{"version":1,"nextFileId":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--source-file=" + filename, "--target-prefix=metadata", "--dry-run"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "reserved _vfs-link prefix") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestMakeImportSnapshotSkipsExpiredEphemeralEntities(t *testing.T) {
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	legacy := legacySnapshot{
		NextFileID: 8,
		Shares:     []db.ShareRecord{{ID: "share-1"}},
		DAVLocks: []db.DAVLockRecord{
			{Token: "active", ExpiresAt: now.Add(time.Hour)},
			{Token: "expired", ExpiresAt: now.Add(-time.Hour)},
		},
		Uploads: []db.UploadRecord{
			{ID: "active", ExpiresAt: now.Add(time.Hour)},
			{ID: "expired", ExpiresAt: now.Add(-time.Hour)},
		},
	}
	snapshot, locks, uploads := makeImportSnapshot(legacy, "abc", 99, now)
	if snapshot.NextFileID != 8 || snapshot.SourceSHA256 != "abc" || snapshot.SourceGeneration != 99 {
		t.Fatalf("snapshot source = %#v", snapshot)
	}
	if len(snapshot.Shares) != 1 || locks != 1 || uploads != 1 || snapshot.DAVLocks[0].Token != "active" || snapshot.Uploads[0].ID != "active" {
		t.Fatalf("snapshot entities = %#v", snapshot)
	}
}

func TestValidateLegacyRejectsDuplicateID(t *testing.T) {
	snapshot, err := decodeLegacy(strings.NewReader(`{"version":1,"nextFileId":3,"files":[{"id":1,"logicPath":"/a","physicalHash":"a"},{"id":1,"logicPath":"/b","physicalHash":"b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateLegacy(snapshot); err == nil || !strings.Contains(err.Error(), "duplicate file id") {
		t.Fatalf("validateLegacy() error = %v", err)
	}
}

func TestDecodeLegacyRejectsTrailingJSON(t *testing.T) {
	if _, err := decodeLegacy(strings.NewReader(`{"version":1,"nextFileId":1} {}`)); err == nil {
		t.Fatal("expected trailing JSON error")
	}
}
