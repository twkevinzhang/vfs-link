package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ExportTreeSnapshot creates a deterministic, importable snapshot of a tree
// store. The caller must still place the service in maintenance mode so share
// and upload entities cannot change while the snapshot is being collected.
func ExportTreeSnapshot(ctx context.Context, store Store) (TreeImportSnapshot, error) {
	tree, ok := store.(*TreeStore)
	if !ok {
		return TreeImportSnapshot{}, fmt.Errorf("tree store is required")
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	release, _, err := tree.acquireTreeMutationLease(ctx)
	if err != nil {
		return TreeImportSnapshot{}, err
	}
	defer release()

	if operations, err := tree.ListRunnableOperations(ctx); err != nil {
		return TreeImportSnapshot{}, fmt.Errorf("inspect operations: %w", err)
	} else if len(operations) != 0 {
		return TreeImportSnapshot{}, fmt.Errorf("tree has %d pending or running operations", len(operations))
	}

	var snapshot TreeImportSnapshot
	if snapshot.Operations, err = tree.listOperations(ctx); err != nil {
		return snapshot, fmt.Errorf("list operations: %w", err)
	}
	active, err := tree.exportActiveRecords(ctx)
	if err != nil {
		return snapshot, fmt.Errorf("list active records: %w", err)
	}
	snapshot.Records = append(snapshot.Records, active...)

	manifests, err := tree.listTrashManifests(ctx)
	if err != nil {
		return snapshot, fmt.Errorf("list trash manifests: %w", err)
	}
	trashIDs := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		trashIDs = append(trashIDs, manifest.ID)
	}
	if len(trashIDs) != 0 {
		trash, err := tree.ListTrashRecords(ctx, trashIDs)
		if err != nil {
			return snapshot, fmt.Errorf("list trash records: %w", err)
		}
		snapshot.Records = append(snapshot.Records, trash...)
	}

	shares, err := tree.listEntities(ctx, "shares", func() any { return &ShareRecord{} })
	if err != nil {
		return snapshot, fmt.Errorf("list shares: %w", err)
	}
	for _, value := range shares {
		snapshot.Shares = append(snapshot.Shares, *(value.(*ShareRecord)))
	}

	now := time.Now()
	locks, err := tree.allDAVLocks(ctx)
	if err != nil {
		return snapshot, fmt.Errorf("list DAV locks: %w", err)
	}
	for _, lock := range locks {
		if lock.ExpiresAt.After(now) {
			snapshot.DAVLocks = append(snapshot.DAVLocks, lock)
		}
	}

	uploads, err := tree.listEntities(ctx, "uploads", func() any { return &UploadRecord{} })
	if err != nil {
		return snapshot, fmt.Errorf("list uploads: %w", err)
	}
	for _, value := range uploads {
		upload := *(value.(*UploadRecord))
		if upload.ExpiresAt.After(now) {
			snapshot.Uploads = append(snapshot.Uploads, upload)
		}
	}

	thumbnails, err := tree.thumbnailRecords(ctx)
	if err != nil {
		return snapshot, fmt.Errorf("list thumbnails: %w", err)
	}
	snapshot.Thumbnails = append(snapshot.Thumbnails, thumbnails...)
	thumbnailLinks, err := tree.listEntities(ctx, fileThumbnailEntityKind, func() any { return &FileThumbnailLink{} })
	if err != nil {
		return snapshot, fmt.Errorf("list thumbnail links: %w", err)
	}
	for _, value := range thumbnailLinks {
		snapshot.ThumbnailLinks = append(snapshot.ThumbnailLinks, *(value.(*FileThumbnailLink)))
	}

	sequenceObject, found, err := tree.objects.Get(ctx, tree.sequenceKey())
	if err != nil {
		return snapshot, fmt.Errorf("read file sequence: %w", err)
	}
	if found {
		var sequence fileSequence
		if err := json.Unmarshal(sequenceObject.Data, &sequence); err != nil {
			return snapshot, fmt.Errorf("decode file sequence: %w", err)
		}
		snapshot.NextFileID = sequence.Next
	}
	normalizeExportSnapshot(&snapshot)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return snapshot, fmt.Errorf("encode snapshot fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	snapshot.SourceSHA256 = hex.EncodeToString(digest[:])
	return snapshot, nil
}

func (s *TreeStore) exportActiveRecords(ctx context.Context) ([]FileRecord, error) {
	keys, err := s.objects.List(ctx, s.prefix+"/tree/nodes/")
	if err != nil {
		return nil, err
	}
	records := make([]FileRecord, len(keys))
	tasks := make([]func(context.Context) error, 0, len(keys))
	for index, key := range keys {
		index, key := index, key
		tasks = append(tasks, func(taskCtx context.Context) error {
			object, found, err := s.objects.Get(taskCtx, key)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("active metadata node disappeared during export: %s", key)
			}
			record, err := decodeTreeRecord(object)
			if err != nil {
				return err
			}
			records[index] = record
			return nil
		})
	}
	if err := runTreeImportTasks(ctx, 32, tasks); err != nil {
		return nil, err
	}
	return records, nil
}

func normalizeExportSnapshot(snapshot *TreeImportSnapshot) {
	sort.Slice(snapshot.Records, func(i, j int) bool {
		if snapshot.Records[i].LogicPath == snapshot.Records[j].LogicPath {
			return snapshot.Records[i].ID < snapshot.Records[j].ID
		}
		return snapshot.Records[i].LogicPath < snapshot.Records[j].LogicPath
	})
	sort.Slice(snapshot.Shares, func(i, j int) bool { return snapshot.Shares[i].ID < snapshot.Shares[j].ID })
	sort.Slice(snapshot.DAVLocks, func(i, j int) bool { return snapshot.DAVLocks[i].Token < snapshot.DAVLocks[j].Token })
	sort.Slice(snapshot.Uploads, func(i, j int) bool { return snapshot.Uploads[i].ID < snapshot.Uploads[j].ID })
	sort.Slice(snapshot.Thumbnails, func(i, j int) bool { return snapshot.Thumbnails[i].ID < snapshot.Thumbnails[j].ID })
	sort.Slice(snapshot.ThumbnailLinks, func(i, j int) bool { return snapshot.ThumbnailLinks[i].FileID < snapshot.ThumbnailLinks[j].FileID })
	if snapshot.NextFileID > 0 {
		return
	}
	for _, record := range snapshot.Records {
		if record.ID >= snapshot.NextFileID {
			snapshot.NextFileID = record.ID + 1
		}
	}
	if snapshot.NextFileID < 1 {
		snapshot.NextFileID = 1
	}
}
