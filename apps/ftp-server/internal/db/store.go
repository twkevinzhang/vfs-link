package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("file mapping not found")

type FileRecord struct {
	ID           int
	LogicPath    string
	PhysicalHash string
	Size         int64
	IsDirectory  bool
	UpdatedAt    time.Time
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
`)
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
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
		if _, err := tx.Exec(ctx, `
INSERT INTO "File" ("logicPath", "physicalHash", size, "isDirectory", "updatedAt")
VALUES ($1, $2, $3, false, now())
`, logicPath, physicalHash, size); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `
UPDATE "File"
SET "physicalHash" = $1,
  size = $2,
  "isDirectory" = false,
  "updatedAt" = now()
WHERE "logicPath" = $3
`, physicalHash, size, logicPath); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if previousIsDirectory || previousPhysicalHash == physicalHash {
		return "", nil
	}
	return previousPhysicalHash, nil
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

func withTrailingSlash(value string) string {
	if value == "/" {
		return "/"
	}
	if value[len(value)-1] == '/' {
		return value
	}
	return value + "/"
}
