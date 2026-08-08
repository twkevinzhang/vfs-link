package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *PostgresStore) Find(ctx context.Context, logicPath string) (FileRecord, bool, error) {
	var err error
	logicPath, err = parseLogicPath(logicPath)
	if err != nil || logicPath == "" {
		return FileRecord{}, false, err
	}
	row := s.pool.QueryRow(ctx, `
SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt"
FROM "File"
WHERE "logicPath" = $1
  AND "trashedAt" IS NULL
`, logicPath)

	record, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return FileRecord{}, false, nil
	}
	if err != nil {
		return FileRecord{}, false, err
	}
	return record, true, nil
}

func (s *PostgresStore) ListPrefix(ctx context.Context, prefix string) ([]FileRecord, error) {
	var parseErr error
	prefix, parseErr = parseLogicPrefix(prefix)
	if parseErr != nil {
		return nil, parseErr
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt"
FROM "File"
WHERE left("logicPath", char_length($1)) = $1
  AND "trashedAt" IS NULL
ORDER BY "logicPath"
`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []FileRecord
	for rows.Next() {
		record, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *PostgresStore) ListAll(ctx context.Context) ([]FileRecord, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt"
FROM "File"
WHERE "trashedAt" IS NULL
ORDER BY "logicPath"
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []FileRecord
	for rows.Next() {
		record, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *PostgresStore) ListDirectChildren(ctx context.Context, dirPath string, options DirectChildrenOptions) (DirectChildrenPage, error) {
	var parseErr error
	dirPath, parseErr = parseLogicPath(dirPath)
	if parseErr != nil {
		return DirectChildrenPage{}, parseErr
	}
	prefix := withTrailingSlash(dirPath)
	filters := []string{`suffix <> ''`, `position('/' in suffix) = 0`}
	args := []any{prefix, dirPath}

	if options.DirectoriesOnly {
		filters = append(filters, `"isDirectory" = true`)
	}

	query := strings.TrimSpace(strings.ToLower(options.Query))
	if query != "" {
		args = append(args, "%"+query+"%")
		placeholder := fmt.Sprintf("$%d", len(args))
		filters = append(filters, fmt.Sprintf(`(lower(suffix) LIKE %s OR lower("logicPath") LIKE %s)`, placeholder, placeholder))
	}

	whereClause := strings.Join(filters, " AND ")
	baseSQL := `
WITH direct AS (
  SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt",
    substring("logicPath" from char_length($1) + 1) AS suffix
  FROM "File"
  WHERE "logicPath" <> $2
    AND left("logicPath", char_length($1)) = $1
    AND "trashedAt" IS NULL
)
`

	var page DirectChildrenPage
	var err error
	page.FolderSummary, err = s.postgresFolderSummary(ctx, dirPath)
	if err != nil {
		return DirectChildrenPage{}, err
	}
	countSQL := fmt.Sprintf(`%s
SELECT count(*), coalesce(sum(CASE WHEN "isDirectory" THEN 0 ELSE size END), 0)::bigint
FROM direct
WHERE %s
`, baseSQL, whereClause)
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total, &page.TotalBytes); err != nil {
		return DirectChildrenPage{}, err
	}
	page.Total = int(total)
	if page.Total == 0 {
		return page, nil
	}

	limitClause := ""
	recordArgs := append([]any{}, args...)
	if options.Limit > 0 {
		limit := options.Limit
		offset := options.Offset
		if offset < 0 {
			offset = 0
		}
		recordArgs = append(recordArgs, limit, offset)
		limitClause = fmt.Sprintf("LIMIT $%d OFFSET $%d", len(recordArgs)-1, len(recordArgs))
	}

	recordsSQL := fmt.Sprintf(`%s
SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt"
FROM direct
WHERE %s
ORDER BY "isDirectory" DESC, suffix
%s
`, baseSQL, whereClause, limitClause)

	rows, err := s.pool.Query(ctx, recordsSQL, recordArgs...)
	if err != nil {
		return DirectChildrenPage{}, err
	}
	defer rows.Close()

	for rows.Next() {
		record, err := scanFile(rows)
		if err != nil {
			return DirectChildrenPage{}, err
		}
		page.Records = append(page.Records, record)
	}
	if err := rows.Err(); err != nil {
		return DirectChildrenPage{}, err
	}
	rows.Close()
	if err := s.hydratePostgresDirectorySummaries(ctx, page.Records); err != nil {
		return DirectChildrenPage{}, err
	}
	return page, nil
}

func (s *PostgresStore) postgresFolderSummary(ctx context.Context, dirPath string) (FolderSummary, error) {
	var summary FolderSummary
	err := s.pool.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE NOT "isDirectory")::bigint,
  count(*) FILTER (WHERE "isDirectory")::bigint,
  coalesce(sum(size) FILTER (WHERE NOT "isDirectory"), 0)::bigint
FROM "File"
WHERE "logicPath" LIKE $1 ESCAPE E'\\'
  AND "trashedAt" IS NULL
`, postgresDescendantPattern(dirPath)).Scan(&summary.Files, &summary.Directories, &summary.Bytes)
	if err != nil {
		return FolderSummary{}, err
	}
	return summary, nil
}

func (s *PostgresStore) hydratePostgresDirectorySummaries(ctx context.Context, records []FileRecord) error {
	paths := make([]string, 0, len(records))
	patterns := make([]string, 0, len(records))
	indices := make(map[string]int, len(records))
	for i := range records {
		if !records[i].IsDirectory {
			continue
		}
		paths = append(paths, records[i].LogicPath)
		patterns = append(patterns, postgresDescendantPattern(records[i].LogicPath))
		indices[records[i].LogicPath] = i
	}
	if len(paths) == 0 {
		return nil
	}

	rows, err := s.pool.Query(ctx, `
WITH directories AS (
  SELECT *
  FROM unnest($1::text[], $2::text[]) AS directory(path, pattern)
)
SELECT directory.path,
  summary.files,
  summary.directories,
  summary.bytes
FROM directories AS directory
CROSS JOIN LATERAL (
  SELECT
    count(*) FILTER (WHERE NOT child."isDirectory")::bigint AS files,
    count(*) FILTER (WHERE child."isDirectory")::bigint AS directories,
    coalesce(sum(child.size) FILTER (WHERE NOT child."isDirectory"), 0)::bigint AS bytes
  FROM "File" AS child
  WHERE child."logicPath" LIKE directory.pattern ESCAPE E'\\'
    AND child."trashedAt" IS NULL
) AS summary
`, paths, patterns)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var path string
		var summary FolderSummary
		if err := rows.Scan(&path, &summary.Files, &summary.Directories, &summary.Bytes); err != nil {
			return err
		}
		index, found := indices[path]
		if !found {
			return fmt.Errorf("unexpected directory summary for %q", path)
		}
		records[index].FolderSummary = &summary
	}
	return rows.Err()
}

func postgresDescendantPattern(dirPath string) string {
	prefix := withTrailingSlash(dirPath)
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	).Replace(prefix)
	return escaped + "%"
}

func (s *PostgresStore) UpsertFile(ctx context.Context, logicPath, physicalHash string, size int64) error {
	var parseErr error
	logicPath, parseErr = parseLogicPath(logicPath)
	if parseErr != nil {
		return parseErr
	}
	if logicPath == "" {
		return fmt.Errorf("file path is required")
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO "File" ("logicPath", "physicalHash", size, "isDirectory", "updatedAt")
VALUES ($1, $2, $3, false, now())
ON CONFLICT ("logicPath") WHERE "trashedAt" IS NULL
DO UPDATE SET
  "physicalHash" = EXCLUDED."physicalHash",
  size = EXCLUDED.size,
  "isDirectory" = false,
  "updatedAt" = now()
`, logicPath, physicalHash, size)
	return err
}

func (s *PostgresStore) ReplaceFile(ctx context.Context, logicPath, physicalHash string, size int64) (string, error) {
	previous, matched, err := s.ReplaceFileConditional(ctx, logicPath, physicalHash, size, nil, false)
	if err != nil {
		return "", err
	}
	if !matched {
		return "", errors.New("unconditional file replacement did not match")
	}
	return previous, nil
}

// ReplaceFileConditional atomically replaces a mapping. An expected object key
// requires the current file to match it; requireAbsent implements create-only.
func (s *PostgresStore) ReplaceFileConditional(
	ctx context.Context,
	logicPath, physicalHash string,
	size int64,
	expectedPhysicalHash *string,
	requireAbsent bool,
) (string, bool, error) {
	var parseErr error
	logicPath, parseErr = parseLogicPath(logicPath)
	if parseErr != nil {
		return "", false, parseErr
	}
	if logicPath == "" {
		return "", false, fmt.Errorf("file path is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)

	var previousPhysicalHash string
	var previousIsDirectory bool
	err = tx.QueryRow(ctx, `
SELECT "physicalHash", "isDirectory"
FROM "File"
WHERE "logicPath" = $1
  AND "trashedAt" IS NULL
FOR UPDATE
`, logicPath).Scan(&previousPhysicalHash, &previousIsDirectory)
	if errors.Is(err, pgx.ErrNoRows) {
		if expectedPhysicalHash != nil {
			return "", false, nil
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO "File" ("logicPath", "physicalHash", size, "isDirectory", "updatedAt")
VALUES ($1, $2, $3, false, now())
`, logicPath, physicalHash, size); err != nil {
			return "", false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	if previousIsDirectory {
		return "", false, ErrIsDirectory
	}
	if requireAbsent || (expectedPhysicalHash != nil && previousPhysicalHash != *expectedPhysicalHash) {
		return "", false, nil
	}

	if _, err := tx.Exec(ctx, `
UPDATE "File"
SET "physicalHash" = $1,
  size = $2,
  "isDirectory" = false,
  "updatedAt" = now()
WHERE "logicPath" = $3
  AND "trashedAt" IS NULL
`, physicalHash, size, logicPath); err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	if previousPhysicalHash == physicalHash {
		return "", true, nil
	}
	return previousPhysicalHash, true, nil
}

func (s *PostgresStore) UpsertDirectory(ctx context.Context, logicPath string) error {
	var parseErr error
	logicPath, parseErr = parseLogicPath(logicPath)
	if parseErr != nil {
		return parseErr
	}
	if logicPath == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO "File" ("logicPath", "physicalHash", size, "isDirectory", "updatedAt")
VALUES ($1, '', 0, true, now())
ON CONFLICT ("logicPath") WHERE "trashedAt" IS NULL
DO UPDATE SET
  "isDirectory" = true,
  "updatedAt" = now()
`, logicPath)
	return err
}

func (s *PostgresStore) RenamePath(ctx context.Context, fromPath, toPath string) error {
	if toPath == fromPath || strings.HasPrefix(toPath, withTrailingSlash(fromPath)) {
		return ErrInvalidMove
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id int
	var isDirectory bool
	err = tx.QueryRow(ctx, `
SELECT id, "isDirectory"
FROM "File"
WHERE "logicPath" = $1
  AND "trashedAt" IS NULL
`, fromPath).Scan(&id, &isDirectory)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	var targetExists bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM "File"
  WHERE "logicPath" = $1
    AND "trashedAt" IS NULL
)
`, toPath).Scan(&targetExists); err != nil {
		return err
	}
	if targetExists {
		return fmt.Errorf("%w: %s", ErrPathConflict, toPath)
	}

	if _, err := tx.Exec(ctx, `
UPDATE "File"
SET "logicPath" = $1, "updatedAt" = now()
WHERE id = $2
`, toPath, id); err != nil {
		return postgresRenameError(err, toPath)
	}

	if isDirectory {
		oldPrefix := withTrailingSlash(fromPath)
		newPrefix := withTrailingSlash(toPath)
		children, err := tx.Query(ctx, `
SELECT id, "logicPath"
FROM "File"
WHERE left("logicPath", char_length($1)) = $1
  AND "trashedAt" IS NULL
ORDER BY "logicPath"
`, oldPrefix)
		if err != nil {
			return err
		}
		defer children.Close()

		type childRecord struct {
			id        int
			logicPath string
		}
		var childRows []childRecord
		for children.Next() {
			var child childRecord
			if err := children.Scan(&child.id, &child.logicPath); err != nil {
				return err
			}
			childRows = append(childRows, child)
		}
		if err := children.Err(); err != nil {
			return err
		}

		for _, child := range childRows {
			newChildPath := newPrefix + strings.TrimPrefix(child.logicPath, oldPrefix)
			if _, err := tx.Exec(ctx, `
UPDATE "File"
SET "logicPath" = $1, "updatedAt" = now()
WHERE id = $2
`, newChildPath, child.id); err != nil {
				return postgresRenameError(err, newChildPath)
			}
		}
	}

	return tx.Commit(ctx)
}

func postgresRenameError(err error, targetPath string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrPathConflict, targetPath)
	}
	return err
}

func (s *PostgresStore) DeletePath(ctx context.Context, logicPath string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM "File" WHERE "logicPath" = $1 AND "trashedAt" IS NULL`, logicPath)
	return err
}

func (s *PostgresStore) DeletePrefix(ctx context.Context, prefix string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM "File" WHERE left("logicPath", char_length($1)) = $1 AND "trashedAt" IS NULL`, prefix)
	return err
}

func withTrailingSlash(value string) string {
	if value == "" {
		return ""
	}
	if value == "/" {
		return "/"
	}
	if value[len(value)-1] == '/' {
		return value
	}
	return value + "/"
}
