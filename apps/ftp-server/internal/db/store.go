package db

import (
	"context"
	"errors"
	"fmt"
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
WHERE left("logicPath", $2) = $1
ORDER BY "logicPath"
`, prefix, len(prefix))
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
WHERE left("logicPath", $2) = $1
ORDER BY "logicPath"
`, oldPrefix, len(oldPrefix))
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
			newChildPath := newPrefix + child.logicPath[len(oldPrefix):]
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
	_, err := s.pool.Exec(ctx, `DELETE FROM "File" WHERE left("logicPath", $2) = $1`, prefix, len(prefix))
	return err
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

func withTrailingSlash(value string) string {
	if value == "/" {
		return "/"
	}
	if value[len(value)-1] == '/' {
		return value
	}
	return value + "/"
}
