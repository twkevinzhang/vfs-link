package db

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

func normalizeFileIDs(ids []int) []int {
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
	fileIDs = normalizeFileIDs(fileIDs)
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
	fileIDs = normalizeFileIDs(fileIDs)
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
	fileIDs = normalizeFileIDs(fileIDs)
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
	fileThumbnailEntityKind  = "file-thumbnails"
	thumbnailIndexEntityKind = "thumbnail-index"
	thumbnailIndexEntityID   = "state"
	thumbnailGCEntityKind    = "thumbnail-gc"
	thumbnailGCEntityID      = "state"
	thumbnailGCMinInterval   = 24 * time.Hour
)

type thumbnailIndexState struct {
	Version int       `json:"version"`
	ReadyAt time.Time `json:"readyAt"`
}

type thumbnailGCState struct {
	LastCompletedAt time.Time `json:"lastCompletedAt"`
}

func fileThumbnailEntityID(fileID int) string { return strconv.Itoa(fileID) }

// rebuildThumbnailIndex performs the explicit one-off migration from the
// legacy ThumbnailRecord.FileIDs field. It must never be called by normal API
// requests: a file without a thumbnail is common and must remain a direct
// lookup miss, not a collection scan.
func (s *TreeStore) rebuildThumbnailIndex(ctx context.Context) error {
	var state thumbnailIndexState
	ready, err := s.getEntity(ctx, thumbnailIndexEntityKind, thumbnailIndexEntityID, &state)
	if err != nil || ready {
		return err
	}
	legacy, err := s.thumbnailRecords(ctx)
	if err != nil {
		return err
	}
	selected := make(map[int]ThumbnailRecord)
	for _, thumbnail := range legacy {
		for _, fileID := range normalizeFileIDs(thumbnail.FileIDs) {
			current, exists := selected[fileID]
			if !exists || thumbnail.CreatedAt.After(current.CreatedAt) || (thumbnail.CreatedAt.Equal(current.CreatedAt) && thumbnail.ID > current.ID) {
				selected[fileID] = thumbnail
			}
		}
	}
	for fileID, thumbnail := range selected {
		link := FileThumbnailLink{FileID: fileID, ThumbnailID: thumbnail.ID, UpdatedAt: time.Now().UTC()}
		if err = s.putEntity(ctx, fileThumbnailEntityKind, fileThumbnailEntityID(fileID), link, true); err != nil && !errorsIsConflict(err) {
			return err
		}
	}
	state = thumbnailIndexState{Version: 1, ReadyAt: time.Now().UTC()}
	if err = s.putEntity(ctx, thumbnailIndexEntityKind, thumbnailIndexEntityID, state, true); err != nil && !errorsIsConflict(err) {
		return err
	}
	return nil
}

// RebuildTreeThumbnailIndex creates the direct file-thumbnail entities from
// legacy ThumbnailRecord.FileIDs values. It is intended for an operator-run
// migration (or test), not the steady-state request path.
func RebuildTreeThumbnailIndex(ctx context.Context, store Store) error {
	tree, ok := store.(*TreeStore)
	if !ok {
		return fmt.Errorf("tree store is required")
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	release, _, err := tree.acquireTreeMutationLease(ctx)
	if err != nil {
		return err
	}
	defer release()
	return tree.rebuildThumbnailIndex(ctx)
}

// findThumbnailLinks returns direct links for fileIDs. A missing entity simply
// means that file has no thumbnail. Legacy records are repaired only by the
// explicit RebuildTreeThumbnailIndex maintenance operation.
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

// updateThumbnailFileIDs keeps the old forward field useful for legacy export
// and repair. It is deliberately not used to decide whether a thumbnail is
// safe to delete: the direct FileThumbnailLink entities are authoritative and
// an interrupted request may leave this denormalized field temporarily stale.
// A whole request is folded into one CAS per affected thumbnail so a batch does
// not repeatedly rewrite an ever-growing FileIDs array.
func (s *TreeStore) updateThumbnailFileIDs(ctx context.Context, thumbnailID string, add, remove []int) error {
	removeSet := make(map[int]bool, len(remove))
	for _, fileID := range remove {
		removeSet[fileID] = true
	}
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		var thumbnail ThumbnailRecord
		generation, found, err := s.getEntityGeneration(ctx, "thumbnails", thumbnailID, &thumbnail)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("thumbnail %s not found", thumbnailID)
		}
		ids := make([]int, 0, len(thumbnail.FileIDs)+len(add))
		for _, id := range thumbnail.FileIDs {
			if !removeSet[id] {
				ids = append(ids, id)
			}
		}
		ids = append(ids, add...)
		thumbnail.FileIDs = normalizeFileIDs(ids)
		if len(add) > 0 {
			thumbnail.DeleteAfter = nil
		} else if len(thumbnail.FileIDs) == 0 {
			deleteAfter := time.Now().UTC().Add(7 * 24 * time.Hour)
			thumbnail.DeleteAfter = &deleteAfter
		}
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
	fileIDs = normalizeFileIDs(fileIDs)
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
	record.FileIDs = nil
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
	oldFileIDsByThumbnail := make(map[string][]int)
	for _, fileID := range fileIDs {
		old := oldLinks[fileID]
		if err = s.replaceThumbnailLink(ctx, FileThumbnailLink{FileID: fileID, ThumbnailID: record.ID}); err != nil {
			return nil, err
		}
		if old.ThumbnailID != "" && old.ThumbnailID != record.ID {
			oldFileIDsByThumbnail[old.ThumbnailID] = append(oldFileIDsByThumbnail[old.ThumbnailID], fileID)
		}
	}
	if err = s.updateThumbnailFileIDs(ctx, record.ID, fileIDs, nil); err != nil {
		return nil, err
	}
	for oldThumbnailID, removeFileIDs := range oldFileIDsByThumbnail {
		if err = s.updateThumbnailFileIDs(ctx, oldThumbnailID, nil, removeFileIDs); err != nil {
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
	fileIDs = normalizeFileIDs(fileIDs)
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
	fileIDs = normalizeFileIDs(fileIDs)
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
	oldFileIDsByThumbnail := make(map[string][]int)
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
		oldFileIDsByThumbnail[link.ThumbnailID] = append(oldFileIDsByThumbnail[link.ThumbnailID], id)
	}
	for thumbnailID, removeFileIDs := range oldFileIDsByThumbnail {
		if err = s.updateThumbnailFileIDs(ctx, thumbnailID, nil, removeFileIDs); err != nil {
			return nil, err
		}
	}
	// See ReplaceThumbnail: direct link GC is intentionally separate so this
	// operation never needs a global list and cannot delete a still-referenced
	// thumbnail after an interrupted mutation.
	return nil, nil
}

// CleanupExpiredThumbnails is a low-frequency maintenance operation. Unlike
// the API request path it may list thumbnail/link entities. It reconciles the
// denormalized FileIDs repair hint, starts the grace period for any orphan that
// an interrupted request failed to mark, then deletes the physical object
// before its metadata. That order makes every failure retryable on a later run.
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
	references := make(map[string][]int, len(values))
	for _, value := range values {
		link := *(value.(*FileThumbnailLink))
		if link.FileID <= 0 || strings.TrimSpace(link.ThumbnailID) == "" {
			return 0, fmt.Errorf("invalid thumbnail link for file id %d", link.FileID)
		}
		references[link.ThumbnailID] = append(references[link.ThumbnailID], link.FileID)
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
		canonicalFileIDs := normalizeFileIDs(references[thumbnail.ID])
		if len(canonicalFileIDs) > 0 {
			changed := thumbnail.DeleteAfter != nil || !slices.Equal(normalizeFileIDs(thumbnail.FileIDs), canonicalFileIDs)
			if changed {
				thumbnail.FileIDs = canonicalFileIDs
				thumbnail.DeleteAfter = nil
				if err = s.putEntityCAS(ctx, "thumbnails", thumbnail.ID, thumbnail, generation); err != nil {
					return deleted, err
				}
			}
			continue
		}
		if thumbnail.DeleteAfter == nil {
			deleteAfter := now.Add(7 * 24 * time.Hour)
			thumbnail.FileIDs = nil
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
