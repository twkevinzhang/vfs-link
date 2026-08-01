package db

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

func normalizePositiveIDs(ids []int) []int {
	seen := make(map[int]bool, len(ids))
	result := make([]int, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	sort.Ints(result)
	return result
}

func (s *PostgresStore) ReplaceThumbnail(ctx context.Context, record ThumbnailRecord, fileIDs []int) ([]ThumbnailRecord, error) {
	fileIDs = normalizePositiveIDs(fileIDs)
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.PhysicalHash) == "" || len(fileIDs) == 0 {
		return nil, fmt.Errorf("thumbnail id, object, and file ids are required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT DISTINCT t.id FROM "Thumbnail" t JOIN "FileThumbnail" ft ON ft."thumbnailId"=t.id WHERE ft."fileId"=ANY($1)`, fileIDs)
	if err != nil {
		return nil, err
	}
	oldIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		oldIDs = append(oldIDs, id)
	}
	rows.Close()
	if _, err = tx.Exec(ctx, `INSERT INTO "Thumbnail" (id,"physicalHash","contentType",size,width,height,"createdAt") VALUES ($1,$2,$3,$4,$5,$6,$7)`, record.ID, record.PhysicalHash, record.ContentType, record.Size, record.Width, record.Height, record.CreatedAt); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM "FileThumbnail" WHERE "fileId"=ANY($1)`, fileIDs); err != nil {
		return nil, err
	}
	for _, fileID := range fileIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO "FileThumbnail" ("fileId","thumbnailId") VALUES ($1,$2)`, fileID, record.ID); err != nil {
			return nil, err
		}
	}
	orphans := make([]ThumbnailRecord, 0)
	if len(oldIDs) > 0 {
		rows, err = tx.Query(ctx, `DELETE FROM "Thumbnail" t WHERE t.id=ANY($1) AND NOT EXISTS (SELECT 1 FROM "FileThumbnail" ft WHERE ft."thumbnailId"=t.id) RETURNING t.id,t."physicalHash",t."contentType",t.size,t.width,t.height,t."createdAt"`, oldIDs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var item ThumbnailRecord
			if err = rows.Scan(&item.ID, &item.PhysicalHash, &item.ContentType, &item.Size, &item.Width, &item.Height, &item.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			orphans = append(orphans, item)
		}
		rows.Close()
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return orphans, nil
}

func (s *PostgresStore) FindThumbnailsForFiles(ctx context.Context, fileIDs []int) (map[int]ThumbnailRecord, error) {
	result := make(map[int]ThumbnailRecord)
	fileIDs = normalizePositiveIDs(fileIDs)
	if len(fileIDs) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT ft."fileId",t.id,t."physicalHash",t."contentType",t.size,t.width,t.height,t."createdAt" FROM "FileThumbnail" ft JOIN "Thumbnail" t ON t.id=ft."thumbnailId" WHERE ft."fileId"=ANY($1)`, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fileID int
		var item ThumbnailRecord
		if err = rows.Scan(&fileID, &item.ID, &item.PhysicalHash, &item.ContentType, &item.Size, &item.Width, &item.Height, &item.CreatedAt); err != nil {
			return nil, err
		}
		result[fileID] = item
	}
	return result, rows.Err()
}

func (s *PostgresStore) FindThumbnail(ctx context.Context, id string) (ThumbnailRecord, bool, error) {
	var item ThumbnailRecord
	err := s.pool.QueryRow(ctx, `SELECT id,"physicalHash","contentType",size,width,height,"createdAt" FROM "Thumbnail" WHERE id=$1`, id).Scan(&item.ID, &item.PhysicalHash, &item.ContentType, &item.Size, &item.Width, &item.Height, &item.CreatedAt)
	if err == nil {
		return item, true, nil
	}
	if err == pgx.ErrNoRows {
		return ThumbnailRecord{}, false, nil
	}
	return ThumbnailRecord{}, false, err
}

func (s *PostgresStore) DetachThumbnails(ctx context.Context, fileIDs []int) ([]ThumbnailRecord, error) {
	fileIDs = normalizePositiveIDs(fileIDs)
	if len(fileIDs) == 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `DELETE FROM "FileThumbnail" WHERE "fileId"=ANY($1) RETURNING "thumbnailId"`, fileIDs)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	orphans := make([]ThumbnailRecord, 0)
	if len(ids) > 0 {
		rows, err = tx.Query(ctx, `DELETE FROM "Thumbnail" t WHERE t.id=ANY($1) AND NOT EXISTS (SELECT 1 FROM "FileThumbnail" ft WHERE ft."thumbnailId"=t.id) RETURNING t.id,t."physicalHash",t."contentType",t.size,t.width,t.height,t."createdAt"`, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var item ThumbnailRecord
			if err = rows.Scan(&item.ID, &item.PhysicalHash, &item.ContentType, &item.Size, &item.Width, &item.Height, &item.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			orphans = append(orphans, item)
		}
		rows.Close()
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return orphans, nil
}

func (s *TreeStore) thumbnailRecords(ctx context.Context) ([]ThumbnailRecord, error) {
	values, err := s.listEntities(ctx, "thumbnails", func() any { return &ThumbnailRecord{} })
	if err != nil {
		return nil, err
	}
	result := make([]ThumbnailRecord, 0, len(values))
	for _, value := range values {
		result = append(result, *(value.(*ThumbnailRecord)))
	}
	return result, nil
}

const (
	fileThumbnailEntityKind = "file-thumbnails"
	thumbnailGCEntityKind   = "thumbnail-gc"
	thumbnailGCEntityID     = "state"
	thumbnailGCMinInterval  = 24 * time.Hour
)

type thumbnailGCState struct {
	LastCompletedAt time.Time `json:"lastCompletedAt"`
}

func fileThumbnailEntityID(fileID int) string { return strconv.Itoa(fileID) }

// findThumbnailLinks returns direct links for fileIDs. A missing entity simply
// means that file has no thumbnail.
func (s *TreeStore) findThumbnailLinks(ctx context.Context, fileIDs []int) (map[int]FileThumbnailLink, error) {
	result := make(map[int]FileThumbnailLink, len(fileIDs))
	var mu sync.Mutex
	tasks := make([]func(context.Context) error, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		fileID := fileID
		tasks = append(tasks, func(taskCtx context.Context) error {
			var link FileThumbnailLink
			found, err := s.getEntity(taskCtx, fileThumbnailEntityKind, fileThumbnailEntityID(fileID), &link)
			if err != nil || !found {
				return err
			}
			if link.FileID != fileID || strings.TrimSpace(link.ThumbnailID) == "" {
				return fmt.Errorf("invalid thumbnail link for file id %d", fileID)
			}
			mu.Lock()
			result[fileID] = link
			mu.Unlock()
			return nil
		})
	}
	if err := runTreeImportTasks(ctx, 32, tasks); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *TreeStore) replaceThumbnailLink(ctx context.Context, link FileThumbnailLink) error {
	if link.FileID <= 0 || strings.TrimSpace(link.ThumbnailID) == "" {
		return fmt.Errorf("file id and thumbnail id are required")
	}
	link.UpdatedAt = time.Now().UTC()
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		generation, found, err := s.getEntityGeneration(ctx, fileThumbnailEntityKind, fileThumbnailEntityID(link.FileID), &FileThumbnailLink{})
		if err != nil {
			return err
		}
		if !found {
			if err = s.putEntity(ctx, fileThumbnailEntityKind, fileThumbnailEntityID(link.FileID), link, true); err == nil {
				return nil
			}
		} else {
			if err = s.putEntityCAS(ctx, fileThumbnailEntityKind, fileThumbnailEntityID(link.FileID), link, generation); err == nil {
				return nil
			}
		}
		if !errorsIsConflict(err) {
			return err
		}
	}
	return ErrMetadataConflict
}

func (s *TreeStore) deleteThumbnailLink(ctx context.Context, fileID int) (FileThumbnailLink, bool, error) {
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		var link FileThumbnailLink
		generation, found, err := s.getEntityGeneration(ctx, fileThumbnailEntityKind, fileThumbnailEntityID(fileID), &link)
		if err != nil || !found {
			return link, found, err
		}
		if link.FileID != fileID || strings.TrimSpace(link.ThumbnailID) == "" {
			return FileThumbnailLink{}, false, fmt.Errorf("invalid thumbnail link for file id %d", fileID)
		}
		if err = s.objects.Delete(ctx, s.entityKey(fileThumbnailEntityKind, fileThumbnailEntityID(fileID)), &generation); err == nil {
			return link, true, nil
		}
		if !errorsIsConflict(err) {
			return FileThumbnailLink{}, false, err
		}
	}
	return FileThumbnailLink{}, false, ErrMetadataConflict
}

// markThumbnailForDeletion starts the grace period without scanning all link
// entities. CleanupExpiredThumbnails remains the authority that verifies
// whether the thumbnail is truly unreferenced before deleting it.
func (s *TreeStore) markThumbnailForDeletion(ctx context.Context, thumbnailID string, now time.Time) error {
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		var thumbnail ThumbnailRecord
		generation, found, err := s.getEntityGeneration(ctx, "thumbnails", thumbnailID, &thumbnail)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("thumbnail %s not found", thumbnailID)
		}
		deleteAfter := now.Add(7 * 24 * time.Hour)
		thumbnail.DeleteAfter = &deleteAfter
		if err = s.putEntityCAS(ctx, "thumbnails", thumbnailID, thumbnail, generation); err == nil {
			return nil
		}
		if !errorsIsConflict(err) {
			return err
		}
	}
	return ErrMetadataConflict
}

func (s *TreeStore) ReplaceThumbnail(ctx context.Context, record ThumbnailRecord, fileIDs []int) ([]ThumbnailRecord, error) {
	fileIDs = normalizePositiveIDs(fileIDs)
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.PhysicalHash) == "" || len(fileIDs) == 0 {
		return nil, fmt.Errorf("thumbnail id, object, and file ids are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, _, err := s.acquireTreeMutationLease(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	// Persist the new record before publishing any links. An interrupted
	// request may leave it orphaned, which is recoverable by GC, but no direct
	// link can ever point to a record that has not been created.
	record.DeleteAfter = nil
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if err = s.putEntity(ctx, "thumbnails", record.ID, record, true); err != nil {
		return nil, err
	}
	oldLinks, err := s.findThumbnailLinks(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	oldThumbnailIDs := make(map[string]bool)
	for _, fileID := range fileIDs {
		old := oldLinks[fileID]
		if err = s.replaceThumbnailLink(ctx, FileThumbnailLink{FileID: fileID, ThumbnailID: record.ID}); err != nil {
			return nil, err
		}
		if old.ThumbnailID != "" && old.ThumbnailID != record.ID {
			oldThumbnailIDs[old.ThumbnailID] = true
		}
	}
	for oldThumbnailID := range oldThumbnailIDs {
		if err = s.markThumbnailForDeletion(ctx, oldThumbnailID, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	// Orphan collection is intentionally deferred. Checking whether an old
	// thumbnail has a final direct reference would require listing every link;
	// this path must remain O(the affected files).
	return nil, nil
}

func (s *TreeStore) FindThumbnailsForFiles(ctx context.Context, fileIDs []int) (map[int]ThumbnailRecord, error) {
	result := make(map[int]ThumbnailRecord)
	fileIDs = normalizePositiveIDs(fileIDs)
	if len(fileIDs) == 0 {
		return result, nil
	}
	links, err := s.findThumbnailLinks(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	thumbnails := make(map[string]ThumbnailRecord, len(links))
	var mu sync.Mutex
	tasks := make([]func(context.Context) error, 0, len(links))
	for _, link := range links {
		if _, exists := thumbnails[link.ThumbnailID]; exists {
			continue
		}
		// Reserve the key before starting tasks so shared links schedule only one
		// metadata read.
		thumbnails[link.ThumbnailID] = ThumbnailRecord{}
		link := link
		tasks = append(tasks, func(taskCtx context.Context) error {
			thumbnail, found, getErr := s.FindThumbnail(taskCtx, link.ThumbnailID)
			if getErr != nil {
				return getErr
			}
			if !found {
				return fmt.Errorf("thumbnail link for file id %d references missing thumbnail %s", link.FileID, link.ThumbnailID)
			}
			mu.Lock()
			thumbnails[link.ThumbnailID] = thumbnail
			mu.Unlock()
			return nil
		})
	}
	if err := runTreeImportTasks(ctx, 32, tasks); err != nil {
		return nil, err
	}
	for fileID, link := range links {
		result[fileID] = thumbnails[link.ThumbnailID]
	}
	return result, nil
}

func (s *TreeStore) FindThumbnail(ctx context.Context, id string) (ThumbnailRecord, bool, error) {
	var item ThumbnailRecord
	ok, err := s.getEntity(ctx, "thumbnails", id, &item)
	return item, ok, err
}

func (s *TreeStore) DetachThumbnails(ctx context.Context, fileIDs []int) ([]ThumbnailRecord, error) {
	fileIDs = normalizePositiveIDs(fileIDs)
	if len(fileIDs) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, _, err := s.acquireTreeMutationLease(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	links, err := s.findThumbnailLinks(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	oldThumbnailIDs := make(map[string]bool)
	for _, id := range fileIDs {
		link, found := links[id]
		if !found {
			continue
		}
		if _, deleted, deleteErr := s.deleteThumbnailLink(ctx, id); deleteErr != nil {
			return nil, deleteErr
		} else if !deleted {
			continue
		}
		oldThumbnailIDs[link.ThumbnailID] = true
	}
	for thumbnailID := range oldThumbnailIDs {
		if err = s.markThumbnailForDeletion(ctx, thumbnailID, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	// See ReplaceThumbnail: direct link GC is intentionally separate so this
	// operation never needs a global list and cannot delete a still-referenced
	// thumbnail after an interrupted mutation.
	return nil, nil
}

// CleanupExpiredThumbnails is a low-frequency maintenance operation. Unlike
// the API request path it may list thumbnail/link entities. It starts the
// grace period for an interrupted write's orphan and clears a stale deletion
// mark when a canonical link still exists. It then deletes the physical object
// before its metadata, making every failure retryable on a later run.
func (s *TreeStore) CleanupExpiredThumbnails(ctx context.Context, now time.Time, deleteObject func(context.Context, ThumbnailRecord) error) (int, error) {
	if deleteObject == nil {
		return 0, fmt.Errorf("thumbnail object deleter is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, _, err := s.acquireTreeMutationLease(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	var gcState thumbnailGCState
	gcStateGeneration, gcStateFound, err := s.getEntityGeneration(ctx, thumbnailGCEntityKind, thumbnailGCEntityID, &gcState)
	if err != nil {
		return 0, err
	}
	if gcStateFound && now.Before(gcState.LastCompletedAt.Add(thumbnailGCMinInterval)) {
		return 0, nil
	}
	thumbnails, err := s.thumbnailRecords(ctx)
	if err != nil {
		return 0, err
	}
	values, err := s.listEntities(ctx, fileThumbnailEntityKind, func() any { return &FileThumbnailLink{} })
	if err != nil {
		return 0, err
	}
	references := make(map[string]bool, len(values))
	for _, value := range values {
		link := *(value.(*FileThumbnailLink))
		if link.FileID <= 0 || strings.TrimSpace(link.ThumbnailID) == "" {
			return 0, fmt.Errorf("invalid thumbnail link for file id %d", link.FileID)
		}
		references[link.ThumbnailID] = true
	}
	deleted := 0
	for _, thumbnail := range thumbnails {
		generation, found, getErr := s.getEntityGeneration(ctx, "thumbnails", thumbnail.ID, &ThumbnailRecord{})
		if getErr != nil {
			return deleted, getErr
		}
		if !found {
			continue
		}
		if references[thumbnail.ID] {
			if thumbnail.DeleteAfter != nil {
				thumbnail.DeleteAfter = nil
				if err = s.putEntityCAS(ctx, "thumbnails", thumbnail.ID, thumbnail, generation); err != nil {
					return deleted, err
				}
			}
			continue
		}
		if thumbnail.DeleteAfter == nil {
			deleteAfter := now.Add(7 * 24 * time.Hour)
			thumbnail.DeleteAfter = &deleteAfter
			if err = s.putEntityCAS(ctx, "thumbnails", thumbnail.ID, thumbnail, generation); err != nil {
				return deleted, err
			}
			continue
		}
		if thumbnail.DeleteAfter.After(now) {
			continue
		}
		if err = deleteObject(ctx, thumbnail); err != nil {
			return deleted, err
		}
		if err = s.objects.Delete(ctx, s.entityKey("thumbnails", thumbnail.ID), &generation); err != nil {
			return deleted, err
		}
		deleted++
	}
	gcState.LastCompletedAt = now
	if gcStateFound {
		err = s.putEntityCAS(ctx, thumbnailGCEntityKind, thumbnailGCEntityID, gcState, gcStateGeneration)
	} else {
		err = s.putEntity(ctx, thumbnailGCEntityKind, thumbnailGCEntityID, gcState, true)
	}
	if err != nil {
		return deleted, err
	}
	return deleted, nil
}
