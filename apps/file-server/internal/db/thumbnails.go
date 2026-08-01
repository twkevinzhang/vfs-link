package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	record.FileIDs = fileIDs
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	items, err := s.thumbnailRecords(ctx)
	if err != nil {
		return nil, err
	}
	remove := make(map[int]bool, len(fileIDs))
	for _, id := range fileIDs {
		remove[id] = true
	}
	orphans := make([]ThumbnailRecord, 0)
	for _, item := range items {
		kept := item.FileIDs[:0]
		for _, id := range item.FileIDs {
			if !remove[id] {
				kept = append(kept, id)
			}
		}
		item.FileIDs = kept
		generation, ok, e := s.getEntityGeneration(ctx, "thumbnails", item.ID, &ThumbnailRecord{})
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		if len(kept) == 0 {
			if e = s.objects.Delete(ctx, s.entityKey("thumbnails", item.ID), &generation); e != nil {
				return nil, e
			}
			orphans = append(orphans, item)
		} else if e = s.putEntityCAS(ctx, "thumbnails", item.ID, item, generation); e != nil {
			return nil, e
		}
	}
	if err = s.putEntity(ctx, "thumbnails", record.ID, record, true); err != nil {
		return nil, err
	}
	return orphans, nil
}

func (s *TreeStore) FindThumbnailsForFiles(ctx context.Context, fileIDs []int) (map[int]ThumbnailRecord, error) {
	result := make(map[int]ThumbnailRecord)
	wanted := make(map[int]bool)
	for _, id := range normalizeFileIDs(fileIDs) {
		wanted[id] = true
	}
	if len(wanted) == 0 {
		return result, nil
	}
	items, err := s.thumbnailRecords(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		for _, id := range item.FileIDs {
			if wanted[id] {
				result[id] = item
			}
		}
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
	items, err := s.thumbnailRecords(ctx)
	if err != nil {
		return nil, err
	}
	remove := make(map[int]bool)
	for _, id := range fileIDs {
		remove[id] = true
	}
	orphans := make([]ThumbnailRecord, 0)
	for _, item := range items {
		kept := item.FileIDs[:0]
		changed := false
		for _, id := range item.FileIDs {
			if remove[id] {
				changed = true
			} else {
				kept = append(kept, id)
			}
		}
		if !changed {
			continue
		}
		item.FileIDs = kept
		generation, ok, e := s.getEntityGeneration(ctx, "thumbnails", item.ID, &ThumbnailRecord{})
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		if len(kept) == 0 {
			if e = s.objects.Delete(ctx, s.entityKey("thumbnails", item.ID), &generation); e != nil {
				return nil, e
			}
			orphans = append(orphans, item)
		} else if e = s.putEntityCAS(ctx, "thumbnails", item.ID, item, generation); e != nil {
			return nil, e
		}
	}
	return orphans, nil
}
