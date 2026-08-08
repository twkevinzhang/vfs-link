package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateShare(ctx context.Context, record ShareRecord) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
INSERT INTO "Share" (
  id, "logicPath", "physicalHash", "fileName", size,
  "destinationObject", "shareUrl", email, status, error
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '')
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt", "processingBy", "processingUntil"
`, record.ID, record.LogicPath, record.PhysicalHash, record.FileName, record.Size,
		record.DestinationObject, record.ShareURL, record.Email, record.Status)
	return scanShare(row)
}

func (s *PostgresStore) FindShare(ctx context.Context, id string) (ShareRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt", "processingBy", "processingUntil"
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

func (s *PostgresStore) MarkShareUploading(ctx context.Context, id, notificationTarget string) (ShareRecord, error) {
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
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt", "processingBy", "processingUntil"
`, id, notificationTarget)
	return scanShare(row)
}

func (s *PostgresStore) MarkShareUploaded(ctx context.Context, id string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET status = 'completed',
  error = '',
  "updatedAt" = now(),
  "completedAt" = now()
WHERE id = $1
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt", "processingBy", "processingUntil"
`, id)
	return scanShare(row)
}

func (s *PostgresStore) MarkShareNotified(ctx context.Context, id string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET status = 'notified',
  error = '',
  "updatedAt" = now(),
  "notifiedAt" = now()
WHERE id = $1
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt", "processingBy", "processingUntil"
`, id)
	return scanShare(row)
}

func (s *PostgresStore) MarkShareFailed(ctx context.Context, id, status, message string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET status = $2,
  error = $3,
  "updatedAt" = now()
WHERE id = $1
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt", "processingBy", "processingUntil"
`, id, status, message)
	return scanShare(row)
}

func (s *PostgresStore) ClaimShareJob(ctx context.Context, id, leaseOwner string, leaseUntil time.Time) (ShareRecord, bool, error) {
	if strings.TrimSpace(leaseOwner) == "" || !leaseUntil.After(time.Now()) {
		return ShareRecord{}, false, fmt.Errorf("valid share lease owner and future expiry are required")
	}
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET "processingBy" = $2, "processingUntil" = $3, "updatedAt" = now()
WHERE id = $1
  AND ("processingUntil" IS NULL OR "processingUntil" <= now() OR "processingBy" = $2)
  AND status NOT IN ('notified')
RETURNING id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt", "processingBy", "processingUntil"
`, id, leaseOwner, leaseUntil)
	record, err := scanShare(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *PostgresStore) ReleaseShareJob(ctx context.Context, id, leaseOwner string) error {
	_, err := s.pool.Exec(ctx, `
UPDATE "Share" SET "processingBy" = NULL, "processingUntil" = NULL, "updatedAt" = now()
WHERE id = $1 AND "processingBy" = $2
`, id, leaseOwner)
	return err
}
