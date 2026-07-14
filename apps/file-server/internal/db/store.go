package db

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound        = errors.New("file mapping not found")
	ErrDAVLockConflict = errors.New("DAV lock conflicts with an active lock")
	ErrInvalidMove     = errors.New("destination cannot be the source or its descendant")
	ErrIsDirectory     = errors.New("file mapping is a directory")
)

const davLockAdvisoryKey int64 = 0x564653444156

type FileRecord struct {
	ID           int
	LogicPath    string
	PhysicalHash string
	Size         int64
	IsDirectory  bool
	UpdatedAt    time.Time
}

type DirectChildrenOptions struct {
	Query           string
	DirectoriesOnly bool
	Limit           int
	Offset          int
}

type DirectChildrenPage struct {
	Records    []FileRecord
	Total      int
	TotalBytes int64
}

type ShareRecord struct {
	ID                string
	LogicPath         string
	PhysicalHash      string
	FileName          string
	Size              int64
	DestinationObject string
	ShareURL          string
	Email             string
	Status            string
	Error             string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
	NotifiedAt        *time.Time
}

type DAVLockRecord struct {
	Token     string
	Path      string
	Owner     string
	Depth     int
	ExpiresAt time.Time
	CreatedAt time.Time
	HeldBy    *string
	HeldUntil *time.Time
}

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS "File" (
  id SERIAL PRIMARY KEY,
  "logicPath" TEXT NOT NULL UNIQUE,
  "physicalHash" TEXT NOT NULL,
  size BIGINT NOT NULL DEFAULT 0,
  "isDirectory" BOOLEAN NOT NULL DEFAULT false,
  "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS "File_logicPath_idx" ON "File" ("logicPath");

CREATE TABLE IF NOT EXISTS "Share" (
  id TEXT PRIMARY KEY,
  "logicPath" TEXT NOT NULL,
  "physicalHash" TEXT NOT NULL,
  "fileName" TEXT NOT NULL,
  size BIGINT NOT NULL DEFAULT 0,
  "destinationObject" TEXT NOT NULL,
  "shareUrl" TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  "createdAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
  "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
  "completedAt" TIMESTAMPTZ,
  "notifiedAt" TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS "Share_logicPath_idx" ON "Share" ("logicPath");
CREATE INDEX IF NOT EXISTS "Share_status_idx" ON "Share" (status);

CREATE TABLE IF NOT EXISTS "DAVLock" (
  token TEXT PRIMARY KEY,
  path TEXT NOT NULL,
  owner TEXT NOT NULL DEFAULT '',
  depth INTEGER NOT NULL CHECK (depth IN (-1, 0)),
  "expiresAt" TIMESTAMPTZ NOT NULL,
  "createdAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
  "heldBy" TEXT,
  "heldUntil" TIMESTAMPTZ
);
ALTER TABLE "DAVLock" ADD COLUMN IF NOT EXISTS "heldBy" TEXT;
ALTER TABLE "DAVLock" ADD COLUMN IF NOT EXISTS "heldUntil" TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS "DAVLock_path_idx" ON "DAVLock" (path);
CREATE INDEX IF NOT EXISTS "DAVLock_expiresAt_idx" ON "DAVLock" ("expiresAt");
`)
	return err
}

func (s *Store) CreateDAVLock(ctx context.Context, record DAVLockRecord) (DAVLockRecord, error) {
	record.Token = strings.TrimSpace(record.Token)
	record.Path = cleanDAVPath(record.Path)
	if record.Token == "" {
		return DAVLockRecord{}, fmt.Errorf("DAV lock token is required")
	}
	if record.Depth != 0 && record.Depth != -1 {
		return DAVLockRecord{}, fmt.Errorf("DAV lock depth must be 0 or -1")
	}
	if record.ExpiresAt.IsZero() {
		return DAVLockRecord{}, fmt.Errorf("DAV lock expiry is required")
	}
	if !record.ExpiresAt.After(time.Now()) {
		return DAVLockRecord{}, fmt.Errorf("DAV lock expiry must be in the future")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DAVLockRecord{}, err
	}
	defer tx.Rollback(ctx)
	if err := acquireDAVLockAdvisoryLock(ctx, tx); err != nil {
		return DAVLockRecord{}, err
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM "DAVLock"
WHERE "expiresAt" <= now()
  AND ("heldUntil" IS NULL OR "heldUntil" <= now())
`); err != nil {
		return DAVLockRecord{}, err
	}

	active, err := queryDAVLocks(ctx, tx, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE "expiresAt" > now() OR "heldUntil" > now()
FOR UPDATE
`)
	if err != nil {
		return DAVLockRecord{}, err
	}
	for _, existing := range active {
		if existing.Token == record.Token || davLocksConflict(existing, record) {
			return DAVLockRecord{}, ErrDAVLockConflict
		}
	}

	created, err := scanDAVLock(tx.QueryRow(ctx, `
INSERT INTO "DAVLock" (token, path, owner, depth, "expiresAt")
VALUES ($1, $2, $3, $4, $5)
RETURNING token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
`, record.Token, record.Path, record.Owner, record.Depth, record.ExpiresAt))
	if err != nil {
		return DAVLockRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DAVLockRecord{}, err
	}
	return created, nil
}

func (s *Store) FindDAVLock(ctx context.Context, token string) (DAVLockRecord, bool, error) {
	record, err := scanDAVLock(s.pool.QueryRow(ctx, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE token = $1 AND "expiresAt" > now()
`, strings.TrimSpace(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return DAVLockRecord{}, false, nil
	}
	if err != nil {
		return DAVLockRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) ListActiveDAVLocks(ctx context.Context, lockPath string) ([]DAVLockRecord, error) {
	lockPath = cleanDAVPath(lockPath)
	records, err := queryDAVLocks(ctx, s.pool, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE "expiresAt" > now() OR "heldUntil" > now()
ORDER BY path, token
`)
	if err != nil {
		return nil, err
	}
	filtered := records[:0]
	for _, record := range records {
		if davLockCoversPath(record, lockPath) || davPathIsAncestor(lockPath, record.Path) {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

func (s *Store) RefreshDAVLock(ctx context.Context, token string, expiresAt time.Time) (DAVLockRecord, bool, error) {
	if expiresAt.IsZero() {
		return DAVLockRecord{}, false, fmt.Errorf("DAV lock expiry is required")
	}
	if !expiresAt.After(time.Now()) {
		return DAVLockRecord{}, false, fmt.Errorf("DAV lock expiry must be in the future")
	}
	record, err := scanDAVLock(s.pool.QueryRow(ctx, `
UPDATE "DAVLock"
SET "expiresAt" = $2
WHERE token = $1 AND "expiresAt" > now()
RETURNING token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
`, strings.TrimSpace(token), expiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return DAVLockRecord{}, false, nil
	}
	if err != nil {
		return DAVLockRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) DeleteDAVLock(ctx context.Context, token string) (bool, error) {
	result, err := s.pool.Exec(ctx, `
DELETE FROM "DAVLock"
WHERE token = $1
  AND ("heldUntil" IS NULL OR "heldUntil" <= now())
`, strings.TrimSpace(token))
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *Store) CleanupExpiredDAVLocks(ctx context.Context) (int64, error) {
	result, err := s.pool.Exec(ctx, `
DELETE FROM "DAVLock"
WHERE "expiresAt" <= now()
  AND ("heldUntil" IS NULL OR "heldUntil" <= now())
`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (s *Store) ClaimDAVLocks(ctx context.Context, paths []string, tokens []string, claimID string, until time.Time) (bool, error) {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return false, fmt.Errorf("DAV lock claim ID is required")
	}
	if until.IsZero() {
		return false, fmt.Errorf("DAV lock claim expiry is required")
	}
	if !until.After(time.Now()) {
		return false, fmt.Errorf("DAV lock claim expiry must be in the future")
	}
	cleanPaths := make([]string, 0, len(paths))
	for _, lockPath := range paths {
		if strings.TrimSpace(lockPath) == "" {
			continue
		}
		cleanPaths = append(cleanPaths, cleanDAVPath(lockPath))
	}
	providedTokens := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		providedTokens[strings.TrimSpace(token)] = struct{}{}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err := acquireDAVLockAdvisoryLock(ctx, tx); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE "DAVLock"
SET "heldBy" = NULL, "heldUntil" = NULL
WHERE "heldUntil" <= now()
`); err != nil {
		return false, err
	}

	active, err := queryDAVLocks(ctx, tx, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE "expiresAt" > now() OR "heldUntil" > now()
FOR UPDATE
`)
	if err != nil {
		return false, err
	}
	matched := make(map[string]struct{})
	matchedPaths := make([]bool, len(cleanPaths))
	for _, record := range active {
		coversRequestedPath := false
		for index, lockPath := range cleanPaths {
			if davLockCoversPath(record, lockPath) {
				coversRequestedPath = true
				matchedPaths[index] = true
				break
			}
		}
		if !coversRequestedPath {
			continue
		}
		if _, ok := providedTokens[record.Token]; !ok {
			return false, nil
		}
		if record.HeldBy != nil && *record.HeldBy != claimID {
			return false, nil
		}
		matched[record.Token] = struct{}{}
	}
	for _, pathMatched := range matchedPaths {
		if !pathMatched {
			return false, nil
		}
	}

	matchedTokens := make([]string, 0, len(matched))
	for token := range matched {
		matchedTokens = append(matchedTokens, token)
	}
	if len(matchedTokens) > 0 {
		if _, err := tx.Exec(ctx, `
UPDATE "DAVLock"
SET "heldBy" = $1, "heldUntil" = $2
WHERE token = ANY($3)
`, claimID, until, matchedTokens); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ReleaseDAVLockClaim(ctx context.Context, claimID string) error {
	_, err := s.pool.Exec(ctx, `
UPDATE "DAVLock"
SET "heldBy" = NULL, "heldUntil" = NULL
WHERE "heldBy" = $1
`, strings.TrimSpace(claimID))
	return err
}

func (s *Store) Find(ctx context.Context, logicPath string) (FileRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt"
FROM "File"
WHERE "logicPath" = $1
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

func (s *Store) ListPrefix(ctx context.Context, prefix string) ([]FileRecord, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt"
FROM "File"
WHERE left("logicPath", char_length($1)) = $1
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

func (s *Store) ListAll(ctx context.Context) ([]FileRecord, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, "logicPath", "physicalHash", size, "isDirectory", "updatedAt"
FROM "File"
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

func (s *Store) ListDirectChildren(ctx context.Context, dirPath string, options DirectChildrenOptions) (DirectChildrenPage, error) {
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
)
`

	var page DirectChildrenPage
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
	return page, nil
}

func (s *Store) UpsertFile(ctx context.Context, logicPath, physicalHash string, size int64) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO "File" ("logicPath", "physicalHash", size, "isDirectory", "updatedAt")
VALUES ($1, $2, $3, false, now())
ON CONFLICT ("logicPath")
DO UPDATE SET
  "physicalHash" = EXCLUDED."physicalHash",
  size = EXCLUDED.size,
  "isDirectory" = false,
  "updatedAt" = now()
`, logicPath, physicalHash, size)
	return err
}

func (s *Store) ReplaceFile(ctx context.Context, logicPath, physicalHash string, size int64) (string, error) {
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
func (s *Store) ReplaceFileConditional(
	ctx context.Context,
	logicPath, physicalHash string,
	size int64,
	expectedPhysicalHash *string,
	requireAbsent bool,
) (string, bool, error) {
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

func (s *Store) UpsertDirectory(ctx context.Context, logicPath string) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO "File" ("logicPath", "physicalHash", size, "isDirectory", "updatedAt")
VALUES ($1, '', 0, true, now())
ON CONFLICT ("logicPath")
DO UPDATE SET
  "isDirectory" = true,
  "updatedAt" = now()
`, logicPath)
	return err
}

func (s *Store) RenamePath(ctx context.Context, fromPath, toPath string) error {
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
`, fromPath).Scan(&id, &isDirectory)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
UPDATE "File"
SET "logicPath" = $1, "updatedAt" = now()
WHERE id = $2
`, toPath, id); err != nil {
		return err
	}

	if isDirectory {
		oldPrefix := withTrailingSlash(fromPath)
		newPrefix := withTrailingSlash(toPath)
		children, err := tx.Query(ctx, `
SELECT id, "logicPath"
FROM "File"
WHERE left("logicPath", char_length($1)) = $1
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
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) DeletePath(ctx context.Context, logicPath string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM "File" WHERE "logicPath" = $1`, logicPath)
	return err
}

func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM "File" WHERE left("logicPath", char_length($1)) = $1`, prefix)
	return err
}

func (s *Store) CreateShare(ctx context.Context, record ShareRecord) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
INSERT INTO "Share" (
  id, "logicPath", "physicalHash", "fileName", size,
  "destinationObject", "shareUrl", email, status, error
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '')
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt"
`, record.ID, record.LogicPath, record.PhysicalHash, record.FileName, record.Size,
		record.DestinationObject, record.ShareURL, record.Email, record.Status)
	return scanShare(row)
}

func (s *Store) FindShare(ctx context.Context, id string) (ShareRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt"
FROM "Share"
WHERE id = $1
`, id)

	record, err := scanShare(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareRecord{}, false, nil
	}
	if err != nil {
		return ShareRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) MarkShareUploading(ctx context.Context, id, notificationTarget string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET email = $2,
  status = 'uploading',
  error = '',
  "updatedAt" = now(),
  "completedAt" = NULL,
  "notifiedAt" = NULL
WHERE id = $1
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt"
`, id, notificationTarget)
	return scanShare(row)
}

func (s *Store) MarkShareUploaded(ctx context.Context, id string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET status = 'completed',
  error = '',
  "updatedAt" = now(),
  "completedAt" = now()
WHERE id = $1
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt"
`, id)
	return scanShare(row)
}

func (s *Store) MarkShareNotified(ctx context.Context, id string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET status = 'notified',
  error = '',
  "updatedAt" = now(),
  "notifiedAt" = now()
WHERE id = $1
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt"
`, id)
	return scanShare(row)
}

func (s *Store) MarkShareFailed(ctx context.Context, id, status, message string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET status = $2,
  error = $3,
  "updatedAt" = now()
WHERE id = $1
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt"
`, id, status, message)
	return scanShare(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFile(row rowScanner) (FileRecord, error) {
	var record FileRecord
	err := row.Scan(
		&record.ID,
		&record.LogicPath,
		&record.PhysicalHash,
		&record.Size,
		&record.IsDirectory,
		&record.UpdatedAt,
	)
	return record, err
}

func scanShare(row rowScanner) (ShareRecord, error) {
	var record ShareRecord
	err := row.Scan(
		&record.ID,
		&record.LogicPath,
		&record.PhysicalHash,
		&record.FileName,
		&record.Size,
		&record.DestinationObject,
		&record.ShareURL,
		&record.Email,
		&record.Status,
		&record.Error,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.CompletedAt,
		&record.NotifiedAt,
	)
	return record, err
}

func scanDAVLock(row rowScanner) (DAVLockRecord, error) {
	var record DAVLockRecord
	err := row.Scan(
		&record.Token,
		&record.Path,
		&record.Owner,
		&record.Depth,
		&record.ExpiresAt,
		&record.CreatedAt,
		&record.HeldBy,
		&record.HeldUntil,
	)
	return record, err
}

type davLockQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryDAVLocks(ctx context.Context, querier davLockQuerier, query string, args ...any) ([]DAVLockRecord, error) {
	rows, err := querier.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []DAVLockRecord
	for rows.Next() {
		record, err := scanDAVLock(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func acquireDAVLockAdvisoryLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, davLockAdvisoryKey)
	return err
}

func cleanDAVPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return pathpkg.Clean(value)
}

func davLocksConflict(first, second DAVLockRecord) bool {
	if first.Path == second.Path {
		return true
	}
	if first.Depth == -1 && davPathIsAncestor(first.Path, second.Path) {
		return true
	}
	return second.Depth == -1 && davPathIsAncestor(second.Path, first.Path)
}

func davLockCoversPath(record DAVLockRecord, value string) bool {
	value = cleanDAVPath(value)
	return record.Path == value || (record.Depth == -1 && davPathIsAncestor(record.Path, value))
}

func davPathIsAncestor(ancestor, descendant string) bool {
	ancestor = cleanDAVPath(ancestor)
	descendant = cleanDAVPath(descendant)
	if ancestor == descendant {
		return false
	}
	if ancestor == "/" {
		return strings.HasPrefix(descendant, "/")
	}
	return strings.HasPrefix(descendant, ancestor+"/")
}

func withTrailingSlash(value string) string {
	if value == "/" {
		return "/"
	}
	if value[len(value)-1] == '/' {
		return value
	}
	return value + "/"
}
