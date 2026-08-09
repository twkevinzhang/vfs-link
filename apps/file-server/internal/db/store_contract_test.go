package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type storeContractFactory struct {
	name string
	open func(*testing.T) Store
}

func TestStoreContract(t *testing.T) {
	factories := []storeContractFactory{
		{
			name: "tree-v3",
			open: func(t *testing.T) Store {
				t.Helper()
				store, err := NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "_contract-v3")
				if err != nil {
					t.Fatalf("open Tree V3 store: %v", err)
				}
				t.Cleanup(store.Close)
				return store
			},
		},
		{
			name: "tree-v4",
			open: func(t *testing.T) Store {
				t.Helper()
				store, err := NewTreeLocalV4(filepath.Join(t.TempDir(), "metadata"), "_contract-v4", TreeV4Options{
					ShardCount:   4,
					MutationMode: TreeV4MutationScoped,
				})
				if err != nil {
					t.Fatalf("open Tree V4 store: %v", err)
				}
				t.Cleanup(store.Close)
				return store
			},
		},
		postgresStoreContractFactory(),
	}

	for _, factory := range factories {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.open(t)
			runStoreContract(t, store)
		})
	}
}

func runStoreContract(t *testing.T, store Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema first call: %v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema second call: %v", err)
	}

	t.Run("schema and empty state", func(t *testing.T) {
		records, err := store.ListAll(ctx)
		if err != nil {
			t.Fatalf("ListAll empty store: %v", err)
		}
		if len(records) != 0 {
			t.Fatalf("ListAll empty store returned %d records", len(records))
		}
		if _, found, err := store.Find(ctx, "missing.txt"); err != nil || found {
			t.Fatalf("Find missing file = found %t, err %v", found, err)
		}
		if deleted, err := store.DeleteUpload(ctx, "missing-upload"); err != nil || deleted {
			t.Fatalf("DeleteUpload missing = deleted %t, err %v", deleted, err)
		}
	})

	t.Run("file and directory lifecycle", func(t *testing.T) {
		const (
			directory = "contract-files"
			fromPath  = directory + "/a.txt"
			toPath    = directory + "/b.txt"
		)
		if err := store.UpsertDirectory(ctx, directory); err != nil {
			t.Fatalf("UpsertDirectory: %v", err)
		}
		if err := store.UpsertFile(ctx, fromPath, "object-a", 7); err != nil {
			t.Fatalf("UpsertFile: %v", err)
		}

		record := requireFileRecord(t, ctx, store, fromPath)
		if record.ID <= 0 || record.PhysicalHash != "object-a" || record.Size != 7 || record.IsDirectory {
			t.Fatalf("created file = %#v", record)
		}
		records, err := store.ListPrefix(ctx, directory+"/")
		if err != nil || !containsRecordPath(records, fromPath) {
			t.Fatalf("ListPrefix = %#v, err %v", records, err)
		}
		page, err := store.ListDirectChildren(ctx, directory, DirectChildrenOptions{Limit: 10})
		if err != nil || page.Total != 1 || len(page.Records) != 1 || page.Records[0].LogicPath != fromPath {
			t.Fatalf("ListDirectChildren = %#v, err %v", page, err)
		}

		old, err := store.ReplaceFile(ctx, fromPath, "object-b", 8)
		if err != nil || old != "object-a" {
			t.Fatalf("ReplaceFile = old %q, err %v", old, err)
		}
		expected := "object-b"
		old, replaced, err := store.ReplaceFileConditional(ctx, fromPath, "object-c", 9, &expected, false)
		if err != nil || !replaced || old != "object-b" {
			t.Fatalf("ReplaceFileConditional matching = old %q, replaced %t, err %v", old, replaced, err)
		}
		stale := "object-b"
		if old, replaced, err = store.ReplaceFileConditional(ctx, fromPath, "object-stale", 10, &stale, false); err != nil || replaced || old != "" {
			t.Fatalf("ReplaceFileConditional stale = old %q, replaced %t, err %v", old, replaced, err)
		}
		if old, replaced, err = store.ReplaceFileConditional(ctx, fromPath, "object-absent", 10, nil, true); err != nil || replaced || old != "" {
			t.Fatalf("ReplaceFileConditional require absent = old %q, replaced %t, err %v", old, replaced, err)
		}
		if got := requireFileRecord(t, ctx, store, fromPath); got.PhysicalHash != "object-c" || got.Size != 9 {
			t.Fatalf("file after conditional replacements = %#v", got)
		}

		if err := store.RenamePath(ctx, fromPath, toPath); err != nil {
			t.Fatalf("RenamePath: %v", err)
		}
		requireMissingRecord(t, ctx, store, fromPath)
		if got := requireFileRecord(t, ctx, store, toPath); got.ID != record.ID || got.PhysicalHash != "object-c" {
			t.Fatalf("renamed file = %#v", got)
		}
		if err := store.DeletePath(ctx, toPath); err != nil {
			t.Fatalf("DeletePath: %v", err)
		}
		requireMissingRecord(t, ctx, store, toPath)
	})

	t.Run("conflict move and trash", func(t *testing.T) {
		const (
			root        = "contract-lifecycle"
			destination = root + "/destination"
			source      = root + "/source.txt"
			conflict    = root + "/conflict.txt"
			moved       = destination + "/source.txt"
			trashID     = "contract-trash"
		)
		for _, directory := range []string{root, destination} {
			if err := store.UpsertDirectory(ctx, directory); err != nil {
				t.Fatalf("UpsertDirectory %q: %v", directory, err)
			}
		}
		if err := store.UpsertFile(ctx, source, "source-object", 11); err != nil {
			t.Fatalf("UpsertFile source: %v", err)
		}
		if err := store.UpsertFile(ctx, conflict, "conflict-object", 12); err != nil {
			t.Fatalf("UpsertFile conflict: %v", err)
		}
		if err := store.RenamePath(ctx, source, conflict); !errors.Is(err, ErrPathConflict) {
			t.Fatalf("RenamePath conflict error = %v", err)
		}

		movedRecords, err := store.BatchMove(ctx, []string{source}, destination)
		if err != nil || len(movedRecords) != 1 || movedRecords[0].LogicPath != moved {
			t.Fatalf("BatchMove = %#v, err %v", movedRecords, err)
		}
		requireMissingRecord(t, ctx, store, source)
		requireFileRecord(t, ctx, store, moved)

		trashed, err := store.TrashPaths(ctx, []TrashPath{{Path: moved, TrashID: trashID}})
		if err != nil || len(trashed) != 1 || trashed[0].TrashID != trashID {
			t.Fatalf("TrashPaths = %#v, err %v", trashed, err)
		}
		requireMissingRecord(t, ctx, store, moved)
		roots, err := store.ListTrash(ctx)
		if err != nil || len(roots) != 1 || roots[0].TrashID != trashID || roots[0].LogicPath != moved {
			t.Fatalf("ListTrash = %#v, err %v", roots, err)
		}
		records, err := store.ListTrashRecords(ctx, []string{trashID})
		if err != nil || len(records) != 1 || records[0].LogicPath != moved {
			t.Fatalf("ListTrashRecords = %#v, err %v", records, err)
		}

		restored, err := store.RestoreTrash(ctx, []string{trashID})
		if err != nil || len(restored) != 1 || restored[0].LogicPath != moved {
			t.Fatalf("RestoreTrash = %#v, err %v", restored, err)
		}
		requireFileRecord(t, ctx, store, moved)

		if _, err = store.TrashPaths(ctx, []TrashPath{{Path: moved, TrashID: trashID}}); err != nil {
			t.Fatalf("TrashPaths before delete: %v", err)
		}
		claimed, err := store.ClaimTrash(ctx, []string{trashID})
		if err != nil || len(claimed) != 1 || !claimed[0].TrashDeleting {
			t.Fatalf("ClaimTrash = %#v, err %v", claimed, err)
		}
		deleted, err := store.DeleteTrash(ctx, []string{trashID})
		if err != nil || deleted != 1 {
			t.Fatalf("DeleteTrash = deleted %d, err %v", deleted, err)
		}
		if records, err = store.ListTrashRecords(ctx, []string{trashID}); err != nil || len(records) != 0 {
			t.Fatalf("ListTrashRecords after delete = %#v, err %v", records, err)
		}
	})

	t.Run("share and upload round trip", func(t *testing.T) {
		share, err := store.CreateShare(ctx, ShareRecord{
			ID:                "contract-share",
			LogicPath:         "contract-share/file.txt",
			PhysicalHash:      "share-object",
			FileName:          "file.txt",
			Size:              13,
			DestinationObject: "out/file.txt",
			ShareURL:          "https://example.test/share",
			Status:            "draft",
		})
		if err != nil || share.ID != "contract-share" || share.CreatedAt.IsZero() || share.UpdatedAt.IsZero() {
			t.Fatalf("CreateShare = %#v, err %v", share, err)
		}
		if found, ok, err := store.FindShare(ctx, share.ID); err != nil || !ok || found.PhysicalHash != "share-object" {
			t.Fatalf("FindShare = %#v, found %t, err %v", found, ok, err)
		}
		if share, err = store.MarkShareUploading(ctx, share.ID, "notify@example.test"); err != nil || share.Status != "uploading" || share.Email != "notify@example.test" {
			t.Fatalf("MarkShareUploading = %#v, err %v", share, err)
		}
		if share, err = store.MarkShareUploaded(ctx, share.ID); err != nil || share.Status != "completed" || share.CompletedAt == nil {
			t.Fatalf("MarkShareUploaded = %#v, err %v", share, err)
		}
		if share, err = store.MarkShareNotified(ctx, share.ID); err != nil || share.Status != "notified" || share.NotifiedAt == nil {
			t.Fatalf("MarkShareNotified = %#v, err %v", share, err)
		}

		now := time.Now().UTC().Truncate(time.Microsecond)
		expectedHash := "previous-object"
		upload, err := store.CreateUpload(ctx, UploadRecord{
			ID:                   "contract-upload",
			LogicPath:            "contract-upload/file.txt",
			PhysicalHash:         "upload-object",
			Driver:               "contract",
			ContentType:          "text/plain",
			UploadURL:            "https://upload.example.test/session",
			Size:                 21,
			UploadedSize:         3,
			Overwrite:            true,
			ExpectedPhysicalHash: &expectedHash,
			Status:               "uploading",
			CreatedAt:            now,
			UpdatedAt:            now,
			ExpiresAt:            now.Add(time.Hour),
		})
		if err != nil || upload.ID != "contract-upload" || upload.UploadURL == "" {
			t.Fatalf("CreateUpload = %#v, err %v", upload, err)
		}
		found, ok, err := store.FindUpload(ctx, upload.ID)
		if err != nil || !ok || found.UploadedSize != 3 || found.ExpectedPhysicalHash == nil || *found.ExpectedPhysicalHash != expectedHash {
			t.Fatalf("FindUpload = %#v, found %t, err %v", found, ok, err)
		}
		found.UploadedSize = found.Size
		found.Status = "complete"
		previousUpdatedAt := found.UpdatedAt
		updated, err := store.UpdateUpload(ctx, found)
		if err != nil || updated.UploadedSize != updated.Size || updated.Status != "complete" || updated.UpdatedAt.Before(previousUpdatedAt) {
			t.Fatalf("UpdateUpload = %#v, err %v", updated, err)
		}
		if deleted, err := store.DeleteUpload(ctx, upload.ID); err != nil || !deleted {
			t.Fatalf("DeleteUpload existing = deleted %t, err %v", deleted, err)
		}
		if deleted, err := store.DeleteUpload(ctx, upload.ID); err != nil || deleted {
			t.Fatalf("DeleteUpload second = deleted %t, err %v", deleted, err)
		}
	})

	t.Run("share dispatch and worker leases", func(t *testing.T) {
		share, err := store.CreateShare(ctx, ShareRecord{
			ID: "contract-share-dispatch", LogicPath: "dispatch/file.txt", PhysicalHash: "dispatch-object",
			FileName: "file.txt", DestinationObject: "out/dispatch.txt", ShareURL: "https://example.test/dispatch", Status: "draft",
		})
		if err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		share, needed, err := store.RequestShareJob(ctx, share.ID, "target", now)
		if err != nil || !needed || share.DispatchStatus != "pending" || share.StartRequestedAt == nil || share.NextDispatchAt == nil {
			t.Fatalf("RequestShareJob = %#v, needed %t, err %v", share, needed, err)
		}

		const contenders = 12
		results := make(chan int, contenders)
		errorsCh := make(chan error, contenders)
		for i := 0; i < contenders; i++ {
			go func(index int) {
				claimed, claimErr := store.ClaimPendingShareDispatch(ctx, "relay-"+string(rune('a'+index)), now, now.Add(time.Minute), 1)
				if claimErr != nil {
					errorsCh <- claimErr
					return
				}
				results <- len(claimed)
			}(i)
		}
		claimedCount := 0
		for i := 0; i < contenders; i++ {
			select {
			case count := <-results:
				claimedCount += count
			case claimErr := <-errorsCh:
				t.Fatalf("concurrent dispatch claim: %v", claimErr)
			}
		}
		if claimedCount != 1 {
			t.Fatalf("dispatch claims = %d, want 1", claimedCount)
		}

		// The original claim owner is intentionally unknown here; an expired
		// dispatch lease must be claimable by a new relay regardless.
		takeoverAt := now.Add(2 * time.Minute)
		claimed, err := store.ClaimPendingShareDispatch(ctx, "relay-takeover", takeoverAt, takeoverAt.Add(time.Minute), 1)
		if err != nil || len(claimed) != 1 || claimed[0].DispatchAttempts != 2 {
			t.Fatalf("expired dispatch takeover = %#v, err %v", claimed, err)
		}
		if err = store.RetryShareDispatch(ctx, share.ID, "relay-takeover", takeoverAt.Add(time.Second), "temporary"); err != nil {
			t.Fatalf("RetryShareDispatch: %v", err)
		}

		workerAt := time.Now().UTC()
		worker, workerClaimed, err := store.ClaimShareJob(ctx, share.ID, "worker-a", workerAt.Add(100*time.Millisecond))
		if err != nil || !workerClaimed || worker.Status != "uploading" {
			t.Fatalf("ClaimShareJob = %#v, claimed %t, err %v", worker, workerClaimed, err)
		}
		if _, duplicate, err := store.ClaimShareJob(ctx, share.ID, "worker-b", workerAt.Add(time.Minute)); err != nil || duplicate {
			t.Fatalf("active duplicate claimed = %t, err %v", duplicate, err)
		}
		time.Sleep(120 * time.Millisecond)
		expiredAt := time.Now().UTC()
		worker, workerClaimed, err = store.ClaimShareJob(ctx, share.ID, "worker-b", expiredAt.Add(time.Minute))
		if err != nil || !workerClaimed || worker.ProcessingBy == nil || *worker.ProcessingBy != "worker-b" {
			t.Fatalf("expired worker takeover = %#v, claimed %t, err %v", worker, workerClaimed, err)
		}
		if _, err = store.MarkShareUploadedBy(ctx, share.ID, "worker-a"); !errors.Is(err, ErrMetadataConflict) {
			t.Fatalf("stale worker MarkShareUploadedBy error = %v", err)
		}
		if worker, err = store.MarkShareUploadedBy(ctx, share.ID, "worker-b"); err != nil || worker.CompletedAt == nil {
			t.Fatalf("owner MarkShareUploadedBy = %#v, err %v", worker, err)
		}
	})

	t.Run("DAV lock lifecycle", func(t *testing.T) {
		const token = "contract-lock"
		expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Microsecond)
		lock, err := store.CreateDAVLock(ctx, DAVLockRecord{Token: token, Path: "/contract-lock/file.txt", Owner: "contract", Depth: 0, ExpiresAt: expiresAt})
		if err != nil || lock.Token != token || lock.CreatedAt.IsZero() {
			t.Fatalf("CreateDAVLock = %#v, err %v", lock, err)
		}
		if found, ok, err := store.FindDAVLock(ctx, token); err != nil || !ok || found.Path != "/contract-lock/file.txt" {
			t.Fatalf("FindDAVLock = %#v, found %t, err %v", found, ok, err)
		}
		locks, err := store.ListActiveDAVLocks(ctx, "/contract-lock/file.txt")
		if err != nil || len(locks) != 1 || locks[0].Token != token {
			t.Fatalf("ListActiveDAVLocks = %#v, err %v", locks, err)
		}
		if _, err := store.CreateDAVLock(ctx, DAVLockRecord{Token: "contract-lock-conflict", Path: "/contract-lock/file.txt", Depth: 0, ExpiresAt: expiresAt}); !errors.Is(err, ErrDAVLockConflict) {
			t.Fatalf("CreateDAVLock conflict error = %v", err)
		}
		refreshedExpiry := expiresAt.Add(time.Minute)
		if refreshed, ok, err := store.RefreshDAVLock(ctx, token, refreshedExpiry); err != nil || !ok || !refreshed.ExpiresAt.Equal(refreshedExpiry) {
			t.Fatalf("RefreshDAVLock = %#v, found %t, err %v", refreshed, ok, err)
		}
		claimUntil := time.Now().UTC().Add(2 * time.Minute)
		if claimed, err := store.ClaimDAVLocks(ctx, []string{"/contract-lock/file.txt"}, []string{token}, "contract-claim", claimUntil); err != nil || !claimed {
			t.Fatalf("ClaimDAVLocks = claimed %t, err %v", claimed, err)
		}
		if deleted, err := store.DeleteDAVLock(ctx, token); err != nil || deleted {
			t.Fatalf("DeleteDAVLock while claimed = deleted %t, err %v", deleted, err)
		}
		if err := store.ReleaseDAVLockClaim(ctx, "contract-claim"); err != nil {
			t.Fatalf("ReleaseDAVLockClaim: %v", err)
		}
		if deleted, err := store.DeleteDAVLock(ctx, token); err != nil || !deleted {
			t.Fatalf("DeleteDAVLock after release = deleted %t, err %v", deleted, err)
		}
		if _, found, err := store.FindDAVLock(ctx, token); err != nil || found {
			t.Fatalf("FindDAVLock after delete = found %t, err %v", found, err)
		}
	})
}

func requireFileRecord(t *testing.T, ctx context.Context, store Store, path string) FileRecord {
	t.Helper()
	record, found, err := store.Find(ctx, path)
	if err != nil || !found {
		t.Fatalf("Find %q = found %t, err %v", path, found, err)
	}
	return record
}

func requireMissingRecord(t *testing.T, ctx context.Context, store Store, path string) {
	t.Helper()
	if record, found, err := store.Find(ctx, path); err != nil || found {
		t.Fatalf("Find missing %q = %#v, found %t, err %v", path, record, found, err)
	}
}

func containsRecordPath(records []FileRecord, path string) bool {
	for _, record := range records {
		if record.LogicPath == path {
			return true
		}
	}
	return false
}
