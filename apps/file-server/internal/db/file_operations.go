package db

import (
	"context"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func normalizeRoots(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var roots []string
	for _, value := range paths {
		value = pathpkg.Clean("/" + strings.TrimSpace(value))
		if value == "/" {
			return nil, fmt.Errorf("root path cannot be modified")
		}
		if !seen[value] {
			seen[value] = true
			roots = append(roots, value)
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("at least one path is required")
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) < len(roots[j]) })
	var result []string
	for _, candidate := range roots {
		nested := false
		for _, root := range result {
			if strings.HasPrefix(candidate, withTrailingSlash(root)) {
				nested = true
				break
			}
		}
		if !nested {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func moveRecords(active []FileRecord, paths []string, destination string, now time.Time) ([]FileRecord, error) {
	roots, err := normalizeRoots(paths)
	if err != nil {
		return nil, err
	}
	destination = pathpkg.Clean("/" + strings.TrimSpace(destination))
	existing := map[string]FileRecord{}
	for _, record := range active {
		existing[record.LogicPath] = record
	}
	if destination != "/" {
		dir, ok := existing[destination]
		if !ok || !dir.IsDirectory {
			return nil, fmt.Errorf("destination directory not found: %s", destination)
		}
	}
	moving := map[int]string{}
	for _, root := range roots {
		record, ok := existing[root]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, root)
		}
		to := pathpkg.Join(destination, pathpkg.Base(root))
		if to == root || (record.IsDirectory && strings.HasPrefix(to, withTrailingSlash(root))) {
			return nil, ErrInvalidMove
		}
		for _, candidate := range active {
			if candidate.LogicPath == root || (record.IsDirectory && strings.HasPrefix(candidate.LogicPath, withTrailingSlash(root))) {
				suffix := strings.TrimPrefix(candidate.LogicPath, root)
				moving[candidate.ID] = to + suffix
			}
		}
	}
	targets := map[string]int{}
	for id, target := range moving {
		if other, ok := targets[target]; ok && other != id {
			return nil, fmt.Errorf("%w: %s", ErrPathConflict, target)
		}
		targets[target] = id
	}
	for _, record := range active {
		if _, isMoving := moving[record.ID]; !isMoving {
			if _, collision := targets[record.LogicPath]; collision {
				return nil, fmt.Errorf("%w: %s", ErrPathConflict, record.LogicPath)
			}
		}
	}
	var result []FileRecord
	for i := range active {
		if target, ok := moving[active[i].ID]; ok {
			active[i].LogicPath = target
			active[i].UpdatedAt = now
			result = append(result, active[i])
		}
	}
	return result, nil
}

func trashRecords(files []FileRecord, items []TrashPath, now time.Time) ([]FileRecord, error) {
	paths := make([]string, len(items))
	ids := map[string]string{}
	for i, item := range items {
		paths[i] = item.Path
		if strings.TrimSpace(item.TrashID) == "" {
			return nil, fmt.Errorf("trash id is required")
		}
		ids[pathpkg.Clean("/"+strings.TrimSpace(item.Path))] = item.TrashID
	}
	roots, err := normalizeRoots(paths)
	if err != nil {
		return nil, err
	}
	active := map[string]FileRecord{}
	for _, record := range files {
		if record.TrashedAt == nil {
			active[record.LogicPath] = record
		}
	}
	for _, root := range roots {
		if _, ok := active[root]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, root)
		}
	}
	var result []FileRecord
	for i := range files {
		if files[i].TrashedAt != nil {
			continue
		}
		for _, root := range roots {
			if files[i].LogicPath == root || strings.HasPrefix(files[i].LogicPath, withTrailingSlash(root)) {
				at := now
				files[i].TrashedAt = &at
				files[i].TrashID = ids[root]
				files[i].TrashRoot = files[i].LogicPath == root
				files[i].UpdatedAt = now
				result = append(result, files[i])
				break
			}
		}
	}
	return result, nil
}

func restoreRecords(files []FileRecord, ids []string, now time.Time) ([]FileRecord, error) {
	wanted := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("at least one trash id is required")
	}
	active := map[string]bool{}
	found := map[string]bool{}
	targets := map[string]string{}
	for _, record := range files {
		if record.TrashedAt == nil {
			active[record.LogicPath] = true
		}
	}
	for _, record := range files {
		if record.TrashedAt != nil && wanted[record.TrashID] {
			if record.TrashDeleting {
				return nil, ErrTrashBusy
			}
			found[record.TrashID] = true
			if active[record.LogicPath] {
				return nil, fmt.Errorf("%w: %s", ErrPathConflict, record.LogicPath)
			}
			if previous, exists := targets[record.LogicPath]; exists && previous != record.TrashID {
				return nil, fmt.Errorf("%w: %s", ErrPathConflict, record.LogicPath)
			}
			targets[record.LogicPath] = record.TrashID
		}
	}
	for id := range wanted {
		if !found[id] {
			return nil, fmt.Errorf("%w: trash %s", ErrNotFound, id)
		}
	}
	var result []FileRecord
	for i := range files {
		if files[i].TrashedAt != nil && wanted[files[i].TrashID] {
			files[i].TrashedAt = nil
			files[i].TrashID = ""
			files[i].TrashRoot = false
			files[i].TrashDeleting = false
			files[i].UpdatedAt = now
			result = append(result, files[i])
		}
	}
	return result, nil
}

func (s *JSONStore) BatchMove(ctx context.Context, paths []string, destination string) ([]FileRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		active := make([]FileRecord, 0, len(st.Files))
		for _, record := range st.Files {
			if record.TrashedAt == nil {
				active = append(active, record)
			}
		}
		updated, err := moveRecords(active, paths, destination, time.Now().UTC())
		if err != nil {
			return nil, false, err
		}
		byID := map[int]FileRecord{}
		for _, r := range updated {
			byID[r.ID] = r
		}
		for i := range st.Files {
			if r, ok := byID[st.Files[i].ID]; ok {
				st.Files[i] = r
			}
		}
		return updated, len(updated) > 0, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]FileRecord), nil
}

func (s *JSONStore) TrashPaths(ctx context.Context, items []TrashPath) ([]FileRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		updated, err := trashRecords(st.Files, items, time.Now().UTC())
		return updated, len(updated) > 0, err
	})
	if err != nil {
		return nil, err
	}
	return v.([]FileRecord), nil
}

func (s *JSONStore) ListTrash(ctx context.Context) ([]FileRecord, error) {
	records, err := s.ListTrashRecords(ctx, nil)
	if err != nil {
		return nil, err
	}
	result := records[:0]
	for _, r := range records {
		if r.TrashRoot {
			result = append(result, r)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TrashedAt.After(*result[j].TrashedAt) })
	return result, nil
}

func (s *JSONStore) ListTrashRecords(ctx context.Context, ids []string) ([]FileRecord, error) {
	st, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	var result []FileRecord
	for _, r := range st.Files {
		if r.TrashedAt != nil && (len(wanted) == 0 || wanted[r.TrashID]) {
			result = append(result, r)
		}
	}
	return result, nil
}

func (s *JSONStore) RestoreTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		updated, err := restoreRecords(st.Files, ids, time.Now().UTC())
		return updated, len(updated) > 0, err
	})
	if err != nil {
		return nil, err
	}
	return v.([]FileRecord), nil
}

func claimTrashRecords(files []FileRecord, ids []string) ([]FileRecord, error) {
	wanted := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	all := len(ids) == 0
	found := map[string]bool{}
	var selected []FileRecord
	for _, record := range files {
		if record.TrashedAt == nil || (!all && !wanted[record.TrashID]) {
			continue
		}
		found[record.TrashID] = true
		selected = append(selected, record)
	}
	if !all {
		for id := range wanted {
			if !found[id] {
				return nil, fmt.Errorf("%w: trash %s", ErrNotFound, id)
			}
		}
	}
	return selected, nil
}

func (s *JSONStore) ClaimTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		selected, err := claimTrashRecords(st.Files, ids)
		if err != nil {
			return nil, false, err
		}
		selectedIDs := map[int]bool{}
		for _, record := range selected {
			selectedIDs[record.ID] = true
		}
		for i := range st.Files {
			if selectedIDs[st.Files[i].ID] {
				st.Files[i].TrashDeleting = true
			}
		}
		for i := range selected {
			selected[i].TrashDeleting = true
		}
		return selected, len(selected) > 0, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]FileRecord), nil
}

func (s *JSONStore) DeleteTrash(ctx context.Context, ids []string) (int64, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		out := st.Files[:0]
		var n int64
		for _, r := range st.Files {
			if r.TrashedAt != nil && r.TrashDeleting && (len(wanted) == 0 || wanted[r.TrashID]) {
				n++
				continue
			}
			out = append(out, r)
		}
		st.Files = out
		return n, n > 0, nil
	})
	if err != nil {
		return 0, err
	}
	return v.(int64), nil
}

func selectAllFiles(ctx context.Context, tx pgx.Tx) ([]FileRecord, error) {
	rows, err := tx.Query(ctx, `SELECT id,"logicPath","physicalHash",size,"isDirectory","updatedAt","trashedAt",coalesce("trashId",''),"trashRoot","trashDeleting" FROM "File" ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FileRecord
	for rows.Next() {
		var r FileRecord
		if err := rows.Scan(&r.ID, &r.LogicPath, &r.PhysicalHash, &r.Size, &r.IsDirectory, &r.UpdatedAt, &r.TrashedAt, &r.TrashID, &r.TrashRoot, &r.TrashDeleting); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *PostgresStore) BatchMove(ctx context.Context, paths []string, destination string) ([]FileRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	all, err := selectAllFiles(ctx, tx)
	if err != nil {
		return nil, err
	}
	var active []FileRecord
	for _, r := range all {
		if r.TrashedAt == nil {
			active = append(active, r)
		}
	}
	updated, err := moveRecords(active, paths, destination, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	token := uuid.NewString()
	for _, r := range updated {
		if _, err := tx.Exec(ctx, `UPDATE "File" SET "logicPath"=$1 WHERE id=$2`, `/.vfs-moving/`+token+fmt.Sprint(r.ID), r.ID); err != nil {
			return nil, err
		}
	}
	for _, r := range updated {
		if _, err := tx.Exec(ctx, `UPDATE "File" SET "logicPath"=$1,"updatedAt"=$2 WHERE id=$3`, r.LogicPath, r.UpdatedAt, r.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *PostgresStore) TrashPaths(ctx context.Context, items []TrashPath) ([]FileRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	all, err := selectAllFiles(ctx, tx)
	if err != nil {
		return nil, err
	}
	updated, err := trashRecords(all, items, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for _, r := range updated {
		if _, err := tx.Exec(ctx, `UPDATE "File" SET "trashedAt"=$1,"trashId"=$2,"trashRoot"=$3,"updatedAt"=$1 WHERE id=$4`, r.TrashedAt, r.TrashID, r.TrashRoot, r.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *PostgresStore) ListTrash(ctx context.Context) ([]FileRecord, error) {
	records, err := s.queryTrash(ctx, nil, true)
	return records, err
}
func (s *PostgresStore) ListTrashRecords(ctx context.Context, ids []string) ([]FileRecord, error) {
	return s.queryTrash(ctx, ids, false)
}
func (s *PostgresStore) queryTrash(ctx context.Context, ids []string, roots bool) ([]FileRecord, error) {
	query := `SELECT id,"logicPath","physicalHash",size,"isDirectory","updatedAt","trashedAt",coalesce("trashId",''),"trashRoot","trashDeleting" FROM "File" WHERE "trashedAt" IS NOT NULL`
	args := []any{}
	if roots {
		query += ` AND "trashRoot"=true`
	}
	if len(ids) > 0 {
		args = append(args, ids)
		query += ` AND "trashId"=ANY($1)`
	}
	query += ` ORDER BY "trashedAt" DESC,"logicPath"`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FileRecord
	for rows.Next() {
		var r FileRecord
		if err := rows.Scan(&r.ID, &r.LogicPath, &r.PhysicalHash, &r.Size, &r.IsDirectory, &r.UpdatedAt, &r.TrashedAt, &r.TrashID, &r.TrashRoot, &r.TrashDeleting); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *PostgresStore) RestoreTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	all, err := selectAllFiles(ctx, tx)
	if err != nil {
		return nil, err
	}
	updated, err := restoreRecords(all, ids, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for _, r := range updated {
		if _, err := tx.Exec(ctx, `UPDATE "File" SET "trashedAt"=NULL,"trashId"=NULL,"trashRoot"=false,"updatedAt"=$1 WHERE id=$2`, r.UpdatedAt, r.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}
func (s *PostgresStore) ClaimTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	all, err := selectAllFiles(ctx, tx)
	if err != nil {
		return nil, err
	}
	selected, err := claimTrashRecords(all, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range selected {
		if _, err := tx.Exec(ctx, `UPDATE "File" SET "trashDeleting"=true WHERE id=$1`, r.ID); err != nil {
			return nil, err
		}
		r.TrashDeleting = true
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	for i := range selected {
		selected[i].TrashDeleting = true
	}
	return selected, nil
}
func (s *PostgresStore) DeleteTrash(ctx context.Context, ids []string) (int64, error) {
	query := `DELETE FROM "File" WHERE "trashedAt" IS NOT NULL AND "trashDeleting"=true`
	args := []any{}
	if len(ids) > 0 {
		args = append(args, ids)
		query += ` AND "trashId"=ANY($1)`
	}
	tag, err := s.pool.Exec(ctx, query, args...)
	return tag.RowsAffected(), err
}
