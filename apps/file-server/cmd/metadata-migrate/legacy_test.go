package main

import (
	"bytes"
	"context"
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
	if err == nil || !strings.Contains(err.Error(), "reserved _vfs-link-v3 or _vfs-link-v4 prefix") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunV4DryRunAndWriteSafety(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(filename, []byte(`{"version":1,"nextFileId":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{
		"--source-file=" + filename,
		"--target-prefix=_vfs-link-v4",
		"--target-shard-count=64",
		"--target-mutation-mode=scoped",
		"--dry-run",
	}, &output); err != nil {
		t.Fatalf("v4 dry-run error = %v", err)
	}
	if !strings.Contains(output.String(), "dry-run complete; target was not modified") {
		t.Fatalf("v4 dry-run output:\n%s", output.String())
	}

	err := run([]string{"--source-file=" + filename, "--target-prefix=_vfs-link-v4", "--yes"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--rollback-journal is required") {
		t.Fatalf("v4 write without rollback journal error = %v", err)
	}

	for _, shardCount := range []string{"0", "3", "512"} {
		err = run([]string{"--source-file=" + filename, "--target-prefix=_vfs-link-v4", "--target-shard-count=" + shardCount, "--dry-run"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "power of two") {
			t.Fatalf("v4 shard count %s error = %v", shardCount, err)
		}
	}
}

func TestRunDryRunsDistributedV3SourceForV4(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source, err := db.NewTreeLocal(root, "_vfs-link-v3")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := source.UpsertFile(ctx, "report.txt", "objects/report", 12); err != nil {
		t.Fatal(err)
	}
	source.Close()

	var output bytes.Buffer
	if err := run([]string{
		"--source-tree-driver=local",
		"--source-tree-local-root=" + root,
		"--source-tree-prefix=_vfs-link-v3",
		"--target-driver=local",
		"--target-local-root=" + root,
		"--target-prefix=_vfs-link-v4",
		"--dry-run",
	}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "source distributed-tree") || !strings.Contains(output.String(), "files=1") {
		t.Fatalf("v3 to v4 dry-run output:\n%s", output.String())
	}
}

func TestRunMigratesDistributedV3SourceIntoV4WithJournal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source, err := db.NewTreeLocal(root, "_vfs-link-v3")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := source.UpsertDirectory(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	if err := source.UpsertFile(ctx, "docs/report.txt", "objects/report", 12); err != nil {
		t.Fatal(err)
	}
	source.Close()

	journal := filepath.Join(root, "rollback.json")
	var output bytes.Buffer
	if err := run([]string{
		"--source-tree-driver=local",
		"--source-tree-local-root=" + root,
		"--source-tree-prefix=_vfs-link-v3",
		"--target-driver=local",
		"--target-local-root=" + root,
		"--target-prefix=_vfs-link-v4",
		"--target-shard-count=64",
		"--target-reducer-interval=2s",
		"--target-mutation-mode=global",
		"--rollback-journal=" + journal,
		"--yes",
	}, &output); err != nil {
		t.Fatalf("v3 to v4 migration error = %v\n%s", err, output.String())
	}
	target, err := db.NewTreeLocalV4(root, "_vfs-link-v4", db.TreeV4Options{ShardCount: 64, MutationMode: db.TreeV4MutationGlobal})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	record, found, err := target.Find(ctx, "docs/report.txt")
	if err != nil || !found || record.Size != 12 {
		t.Fatalf("v4 Find() = %#v, %t, %v", record, found, err)
	}
	payload, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"status": "completed"`) {
		t.Fatalf("rollback journal was not completed:\n%s", payload)
	}

	retryJournal := filepath.Join(root, "retry-rollback.json")
	err = run([]string{
		"--source-tree-driver=local", "--source-tree-local-root=" + root, "--source-tree-prefix=_vfs-link-v3",
		"--target-driver=local", "--target-local-root=" + root, "--target-prefix=_vfs-link-v4",
		"--rollback-journal=" + retryJournal, "--yes",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "v4 import prefix is not empty") {
		t.Fatalf("v4 target reuse error = %v", err)
	}
	retryPayload, readErr := os.ReadFile(retryJournal)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(retryPayload), `"status": "prepared"`) {
		t.Fatalf("failed retry journal must remain prepared:\n%s", retryPayload)
	}
}

func TestValidateRootAggregatesAfterBulkImport(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(t.TempDir(), "_vfs-link-v3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	records := []db.FileRecord{
		{ID: 1, LogicPath: "/docs", IsDirectory: true},
		{ID: 2, LogicPath: "/docs/archive", IsDirectory: true},
		{ID: 3, LogicPath: "/docs/archive/a.txt", PhysicalHash: "objects/a", Size: 12},
		{ID: 4, LogicPath: "/root.txt", PhysicalHash: "objects/root", Size: 5},
	}
	if _, err := db.BulkImportTree(ctx, store, db.TreeImportSnapshot{Records: records, NextFileID: 5}); err != nil {
		t.Fatal(err)
	}
	summary, err := validateRootAggregates(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	want := db.FolderSummary{Files: 2, Directories: 2, Bytes: 17}
	if summary != want {
		t.Fatalf("root summary = %+v, want %+v", summary, want)
	}
}

func TestRunMigratesDistributedV2TreeIntoRelativeV3Prefix(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source, err := db.NewTreeLocal(root, "_vfs-link")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	trashedAt := time.Date(2026, 7, 17, 1, 0, 0, 0, time.UTC)
	records := []db.FileRecord{
		{ID: 1, LogicPath: "/docs", IsDirectory: true},
		{ID: 2, LogicPath: "/docs/a.txt", PhysicalHash: "objects/a", Size: 12},
		{ID: 3, LogicPath: "/deleted.txt", PhysicalHash: "objects/deleted", Size: 7, TrashedAt: &trashedAt, TrashID: "trash-1", TrashRoot: true},
	}
	if _, err := db.BulkImportTree(ctx, source, db.TreeImportSnapshot{Records: records, NextFileID: 4}); err != nil {
		t.Fatal(err)
	}
	source.Close()

	var output bytes.Buffer
	err = run([]string{
		"--source-tree-driver=local",
		"--source-tree-local-root=" + root,
		"--source-tree-prefix=_vfs-link",
		"--target-driver=local",
		"--target-local-root=" + root,
		"--target-prefix=_vfs-link-v3",
		"--yes",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "source distributed-tree") || !strings.Contains(output.String(), "target root aggregates: files=1 directories=1 bytes=12") {
		t.Fatalf("migration output:\n%s", output.String())
	}

	target, err := db.NewTreeLocal(root, "_vfs-link-v3")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	active, err := target.ListAll(ctx)
	if err != nil || len(active) != 2 {
		t.Fatalf("active records = %d, err = %v", len(active), err)
	}
	for _, record := range active {
		if strings.HasPrefix(record.LogicPath, "/") {
			t.Fatalf("v3 logical path is absolute: %q", record.LogicPath)
		}
	}
	trash, err := target.ListTrashRecords(ctx, []string{"trash-1"})
	if err != nil || len(trash) != 1 || trash[0].ID != 3 {
		t.Fatalf("trash records = %#v, err = %v", trash, err)
	}

	if err := run([]string{
		"--source-tree-driver=local", "--source-tree-local-root=" + root,
		"--target-driver=local", "--target-local-root=" + root, "--yes",
	}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("rerun error = %v, want non-empty target rejection", err)
	}
}

func TestCanonicalizeImportSnapshotNormalizesNFCAndRejectsCollision(t *testing.T) {
	snapshot := db.TreeImportSnapshot{Records: []db.FileRecord{
		{ID: 1, LogicPath: "/cafe\u0301.txt", PhysicalHash: "nfd"},
		{ID: 2, LogicPath: "/caf\u00e9.txt", PhysicalHash: "nfc"},
	}}
	if _, err := canonicalizeImportSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "canonical path collision") {
		t.Fatalf("canonicalize collision error = %v", err)
	}
	snapshot.Records = snapshot.Records[:1]
	canonical, err := canonicalizeImportSnapshot(snapshot)
	if err != nil || canonical.Records[0].LogicPath != "caf\u00e9.txt" {
		t.Fatalf("canonical snapshot = %#v, err = %v", canonical, err)
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
