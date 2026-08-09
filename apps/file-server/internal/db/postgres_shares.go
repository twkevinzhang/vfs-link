package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const shareColumns = `id, "logicPath", "physicalHash", "fileName", size, "destinationObject",
  "shareUrl", email, status, error, "createdAt", "updatedAt", "completedAt", "notifiedAt",
  "processingBy", "processingUntil", "dispatchStatus", "dispatchAttempts", "nextDispatchAt",
  "dispatchLeaseOwner", "dispatchLeaseUntil", "lastDispatchError", "startRequestedAt"`

func (s *PostgresStore) CreateShare(ctx context.Context, record ShareRecord) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
INSERT INTO "Share" (
  id, "logicPath", "physicalHash", "fileName", size,
  "destinationObject", "shareUrl", email, status, error, "dispatchStatus"
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '', $10)
RETURNING `+shareColumns, record.ID, record.LogicPath, record.PhysicalHash, record.FileName, record.Size,
		record.DestinationObject, record.ShareURL, record.Email, record.Status, defaultDispatchStatus(record.DispatchStatus))
	return scanShare(row)
}

// CreateShareFromSnapshot acquires the source object reference only while the
// active mapping still matches the snapshot read by the application service.
// This prevents cleanup from observing no Share and then deleting an object
// that a stale draft would otherwise reference afterward.
func (s *PostgresStore) CreateShareFromSnapshot(ctx context.Context, record ShareRecord) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
WITH source AS (
  SELECT 1 FROM "File"
  WHERE "logicPath"=$2 AND "physicalHash"=$3 AND "trashedAt" IS NULL AND NOT "isDirectory"
  FOR SHARE
)
INSERT INTO "Share" (
  id, "logicPath", "physicalHash", "fileName", size,
  "destinationObject", "shareUrl", email, status, error, "dispatchStatus"
)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,'',$10 FROM source
RETURNING `+shareColumns, record.ID, record.LogicPath, record.PhysicalHash, record.FileName, record.Size,
		record.DestinationObject, record.ShareURL, record.Email, record.Status, defaultDispatchStatus(record.DispatchStatus))
	created, err := scanShare(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareRecord{}, ErrMetadataConflict
	}
	return created, err
}

func defaultDispatchStatus(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

func (s *PostgresStore) FindShare(ctx context.Context, id string) (ShareRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+shareColumns+` FROM "Share" WHERE id = $1`, id)
	record, err := scanShare(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareRecord{}, false, nil
	}
	return record, err == nil, err
}

// RequestShareJob atomically records durable dispatch intent. Business state is
// deliberately left unchanged; only a worker holding the processing lease may
// enter uploading.
func (s *PostgresStore) RequestShareJob(ctx context.Context, id, target string, now time.Time) (ShareRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET email = CASE WHEN status <> 'notified' THEN $2 ELSE email END,
  "dispatchStatus" = CASE
    WHEN status <> 'notified' AND ("processingUntil" IS NULL OR "processingUntil" <= $3) THEN 'pending'
    ELSE "dispatchStatus" END,
  "nextDispatchAt" = CASE
    WHEN status <> 'notified' AND ("processingUntil" IS NULL OR "processingUntil" <= $3) THEN $3
    ELSE "nextDispatchAt" END,
  "startRequestedAt" = CASE
    WHEN status <> 'notified' AND "dispatchStatus" IN ('dispatch_failed', 'dispatch_paused') THEN $3
    WHEN status <> 'notified' THEN COALESCE("startRequestedAt", $3)
    ELSE "startRequestedAt" END,
  "dispatchAttempts" = CASE
    WHEN status <> 'notified' AND "dispatchStatus" IN ('dispatch_failed', 'dispatch_paused') THEN 0
    ELSE "dispatchAttempts" END,
  "lastDispatchError" = CASE WHEN status <> 'notified' THEN '' ELSE "lastDispatchError" END,
  "updatedAt" = CASE WHEN status <> 'notified' THEN $3 ELSE "updatedAt" END
WHERE id = $1
RETURNING `+shareColumns, id, target, now)
	record, err := scanShare(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareRecord{}, false, ErrNotFound
	}
	if err != nil {
		return ShareRecord{}, false, err
	}
	dispatchNeeded := record.Status != "notified" &&
		(record.ProcessingUntil == nil || !record.ProcessingUntil.After(now))
	return record, dispatchNeeded, nil
}

func (s *PostgresStore) ClaimPendingShareDispatch(ctx context.Context, owner string, now, leaseUntil time.Time, limit int) ([]ShareRecord, error) {
	if strings.TrimSpace(owner) == "" || !leaseUntil.After(now) || limit <= 0 {
		return nil, fmt.Errorf("valid dispatch owner, lease, and limit are required")
	}
	rows, err := s.pool.Query(ctx, `
WITH due AS (
  SELECT id
  FROM "Share"
  WHERE status <> 'notified'
    AND ("processingUntil" IS NULL OR "processingUntil" <= $2)
    AND COALESCE("nextDispatchAt", $2) <= $2
    AND (
      "dispatchStatus" = 'pending'
      OR "dispatchStatus" = 'dispatched'
      OR ("dispatchStatus" = 'dispatching' AND ("dispatchLeaseUntil" IS NULL OR "dispatchLeaseUntil" <= $2))
    )
  ORDER BY "nextDispatchAt" NULLS FIRST, "createdAt"
  FOR UPDATE SKIP LOCKED
  LIMIT $4
)
UPDATE "Share" AS s
SET "dispatchStatus" = 'dispatching',
  "dispatchAttempts" = s."dispatchAttempts" + 1,
  "dispatchLeaseOwner" = $1,
  "dispatchLeaseUntil" = $3,
  "updatedAt" = $2
FROM due
WHERE s.id = due.id
RETURNING `+qualifiedShareColumns("s"), owner, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []ShareRecord
	for rows.Next() {
		record, scanErr := scanShare(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func qualifiedShareColumns(alias string) string {
	columns := strings.Split(shareColumns, ",")
	for i := range columns {
		columns[i] = alias + "." + strings.TrimSpace(columns[i])
	}
	return strings.Join(columns, ", ")
}

func (s *PostgresStore) MarkShareDispatched(ctx context.Context, id, owner string, redeliverAt time.Time) error {
	result, err := s.pool.Exec(ctx, `
UPDATE "Share"
SET "dispatchStatus" = CASE WHEN "dispatchStatus" = 'dispatch_paused' THEN 'dispatch_paused' ELSE 'dispatched' END,
  "nextDispatchAt" = CASE WHEN status = 'notified' OR "dispatchStatus" = 'dispatch_paused' THEN NULL ELSE $3 END,
  "dispatchLeaseOwner" = NULL, "dispatchLeaseUntil" = NULL,
  "lastDispatchError" = '', "updatedAt" = now()
WHERE id = $1 AND "dispatchStatus" IN ('dispatching', 'dispatch_paused') AND "dispatchLeaseOwner" = $2
`, id, owner, redeliverAt)
	return requireAffected(result.RowsAffected(), err)
}

func (s *PostgresStore) RetryShareDispatch(ctx context.Context, id, owner string, next time.Time, message string) error {
	result, err := s.pool.Exec(ctx, `
UPDATE "Share"
SET "dispatchStatus" = 'pending', "nextDispatchAt" = $3,
  "dispatchLeaseOwner" = NULL, "dispatchLeaseUntil" = NULL,
  "lastDispatchError" = $4, "updatedAt" = now()
WHERE id = $1 AND "dispatchStatus" = 'dispatching' AND "dispatchLeaseOwner" = $2
`, id, owner, next, message)
	return requireAffected(result.RowsAffected(), err)
}

func (s *PostgresStore) FailShareDispatch(ctx context.Context, id, owner, message string) error {
	result, err := s.pool.Exec(ctx, `
UPDATE "Share"
SET "dispatchStatus" = 'dispatch_failed', "nextDispatchAt" = NULL,
  "dispatchLeaseOwner" = NULL, "dispatchLeaseUntil" = NULL,
  "lastDispatchError" = $3, "updatedAt" = now()
WHERE id = $1 AND "dispatchStatus" = 'dispatching' AND "dispatchLeaseOwner" = $2
`, id, owner, message)
	return requireAffected(result.RowsAffected(), err)
}

func requireAffected(count int64, err error) error {
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrMetadataConflict
	}
	return nil
}

func (s *PostgresStore) MarkShareUploading(ctx context.Context, id, notificationTarget string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Share" SET email = $2, status = 'uploading', error = '', "updatedAt" = now(),
  "completedAt" = NULL, "notifiedAt" = NULL WHERE id = $1 RETURNING `+shareColumns, id, notificationTarget)
	return scanShare(row)
}

func (s *PostgresStore) MarkShareUploaded(ctx context.Context, id string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `UPDATE "Share" SET status = 'completed', error = '', "updatedAt" = now(),
  "completedAt" = COALESCE("completedAt", now()) WHERE id = $1 RETURNING `+shareColumns, id)
	return scanShare(row)
}

func (s *PostgresStore) MarkShareNotified(ctx context.Context, id string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `UPDATE "Share" SET status = 'notified', error = '', "updatedAt" = now(),
  "notifiedAt" = COALESCE("notifiedAt", now()), "dispatchStatus" = 'dispatched', "nextDispatchAt" = NULL
  WHERE id = $1 RETURNING `+shareColumns, id)
	return scanShare(row)
}

func (s *PostgresStore) MarkShareFailed(ctx context.Context, id, status, message string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `UPDATE "Share" SET status = $2, error = $3, "updatedAt" = now()
  WHERE id = $1 RETURNING `+shareColumns, id, status, message)
	return scanShare(row)
}

func (s *PostgresStore) ClaimShareJob(ctx context.Context, id, leaseOwner string, leaseUntil time.Time) (ShareRecord, bool, error) {
	if strings.TrimSpace(leaseOwner) == "" || !leaseUntil.After(time.Now()) {
		return ShareRecord{}, false, fmt.Errorf("valid share lease owner and future expiry are required")
	}
	row := s.pool.QueryRow(ctx, `
UPDATE "Share"
SET "processingBy" = $2, "processingUntil" = $3,
  status = CASE WHEN "completedAt" IS NULL THEN 'uploading' ELSE status END,
  error = CASE WHEN "completedAt" IS NULL THEN '' ELSE error END,
  "updatedAt" = now()
WHERE id = $1
  AND ("processingUntil" IS NULL OR "processingUntil" <= now() OR "processingBy" = $2)
  AND status <> 'notified'
RETURNING `+shareColumns, id, leaseOwner, leaseUntil)
	record, err := scanShare(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *PostgresStore) MarkShareUploadedBy(ctx context.Context, id, owner string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `UPDATE "Share" SET status = 'completed', error = '', "updatedAt" = now(),
  "completedAt" = COALESCE("completedAt", now())
  WHERE id = $1 AND "processingBy" = $2 AND status = 'uploading' RETURNING `+shareColumns, id, owner)
	return scanExpectedShare(row)
}

func (s *PostgresStore) MarkShareNotifiedBy(ctx context.Context, id, owner string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `UPDATE "Share" SET status = 'notified', error = '', "updatedAt" = now(),
	  "notifiedAt" = COALESCE("notifiedAt", now()), "nextDispatchAt" = NULL
	  WHERE id = $1 AND "processingBy" = $2 AND "completedAt" IS NOT NULL AND status <> 'notified'
  RETURNING `+shareColumns, id, owner)
	return scanExpectedShare(row)
}

func (s *PostgresStore) MarkShareFailedBy(ctx context.Context, id, owner, status, message string) (ShareRecord, error) {
	row := s.pool.QueryRow(ctx, `UPDATE "Share" SET status = $3, error = $4, "updatedAt" = now()
  WHERE id = $1 AND "processingBy" = $2 AND status <> 'notified' RETURNING `+shareColumns, id, owner, status, message)
	return scanExpectedShare(row)
}

func (s *PostgresStore) StopShareRedelivery(ctx context.Context, id, owner string) error {
	result, err := s.pool.Exec(ctx, `UPDATE "Share" SET "dispatchStatus" = 'dispatch_paused', "nextDispatchAt" = NULL, "updatedAt" = now()
  WHERE id = $1 AND "processingBy" = $2`, id, owner)
	return requireAffected(result.RowsAffected(), err)
}

func scanExpectedShare(row rowScanner) (ShareRecord, error) {
	record, err := scanShare(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareRecord{}, ErrMetadataConflict
	}
	return record, err
}

func (s *PostgresStore) ReleaseShareJob(ctx context.Context, id, leaseOwner string) error {
	_, err := s.pool.Exec(ctx, `UPDATE "Share" SET "processingBy" = NULL, "processingUntil" = NULL, "updatedAt" = now()
WHERE id = $1 AND "processingBy" = $2`, id, leaseOwner)
	return err
}
