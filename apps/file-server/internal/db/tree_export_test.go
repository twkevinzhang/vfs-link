package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestExportTreeSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := newTestTree(t)
	if err := source.UpsertDirectory(ctx, "archive"); err != nil {
		t.Fatal(err)
	}
	if err := source.UpsertFile(ctx, "archive/live.bin", "live", 11); err != nil {
		t.Fatal(err)
	}
	if err := source.UpsertFile(ctx, "deleted.bin", "deleted", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := source.TrashPaths(ctx, []TrashPath{{Path: "deleted.bin", TrashID: "trash-export"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateShare(ctx, ShareRecord{ID: "share-export", LogicPath: "archive/live.bin", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateDAVLock(ctx, DAVLockRecord{Token: "lock-export", Path: "archive/live.bin", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateUpload(ctx, UploadRecord{ID: "upload-export", LogicPath: "future.bin", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	live, found, err := source.Find(ctx, "archive/live.bin")
	if err != nil || !found {
		t.Fatalf("find live file found=%v err=%v", found, err)
	}
	if _, err = source.ReplaceThumbnail(ctx, ThumbnailRecord{ID: "thumbnail-export", PhysicalHash: "thumbnail-export.webp", ContentType: "image/webp", Size: 12, Width: 8, Height: 6}, []int{live.ID}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := ExportTreeSnapshot(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 3 || len(snapshot.Shares) != 1 || len(snapshot.DAVLocks) != 1 || len(snapshot.Uploads) != 1 || len(snapshot.Thumbnails) != 1 || snapshot.SourceSHA256 == "" {
		t.Fatalf("snapshot=%+v", snapshot)
	}

	target := newEmptyTree(t)
	if _, err := BulkImportTree(ctx, target, snapshot); err != nil {
		t.Fatal(err)
	}
	page, err := target.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 11}) {
		t.Fatalf("summary=%+v", page.FolderSummary)
	}
	importedLive, found, err := target.Find(ctx, "archive/live.bin")
	if err != nil || !found {
		t.Fatalf("find imported live file found=%v err=%v", found, err)
	}
	importedThumbnails, err := target.FindThumbnailsForFiles(ctx, []int{importedLive.ID})
	if err != nil || importedThumbnails[importedLive.ID].ID != "thumbnail-export" {
		t.Fatalf("imported thumbnails=%+v err=%v", importedThumbnails, err)
	}
	trash, err := target.ListTrash(ctx)
	if err != nil || len(trash) != 1 || trash[0].TrashID != "trash-export" {
		t.Fatalf("trash=%+v err=%v", trash, err)
	}
}

func TestExportActiveRecordsReadsEveryNode(t *testing.T) {
	ctx := context.Background()
	source := newTestTree(t)
	if err := source.UpsertDirectory(ctx, "bulk"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 80; index++ {
		path := fmt.Sprintf("bulk/%03d.bin", index)
		if err := source.UpsertFile(ctx, path, path, int64(index)); err != nil {
			t.Fatal(err)
		}
	}
	records, err := source.exportActiveRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 81 {
		t.Fatalf("records=%d, want 81 including /bulk", len(records))
	}
}
