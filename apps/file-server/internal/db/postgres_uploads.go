package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const uploadColumns = `id, "logicPath", "physicalHash", driver, "contentType", "uploadUrl", size,
  "uploadedSize", overwrite, "expectedPhysicalHash", "expectedFileId", "expectedFileUpdatedAt",
  "requireAbsent", status, error, revision,
  "completionStatus", "completionOwner", "completionLeaseUntil", "completionAttempts",
  "completionNextAttemptAt", "finalizedAt", "publishedAt", "completedAt", "objectGeneration",
  "objectChecksum", "lastCompletionError", "cancelRequestedAt", "cancelledAt", "cleanupStatus",
  "previousPhysicalHash", "cleanupError", "createdAt", "updatedAt", "expiresAt"`

func (s *PostgresStore) CreateUpload(ctx context.Context, record UploadRecord) (UploadRecord, error) {
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if record.Revision <= 0 {
		record.Revision = 1
	}
	if strings.TrimSpace(record.CompletionStatus) == "" {
		record.CompletionStatus = "none"
	}
	if strings.TrimSpace(record.CleanupStatus) == "" {
		record.CleanupStatus = "none"
	}
	row := s.pool.QueryRow(ctx, `
INSERT INTO "Upload" (id, "logicPath", "physicalHash", driver, "contentType", "uploadUrl", size,
  "uploadedSize", overwrite, "expectedPhysicalHash", "expectedFileId", "expectedFileUpdatedAt",
  "requireAbsent", status, error, revision,
  "completionStatus", "completionOwner", "completionLeaseUntil", "completionAttempts",
  "completionNextAttemptAt", "finalizedAt", "publishedAt", "completedAt", "objectGeneration",
  "objectChecksum", "lastCompletionError", "cancelRequestedAt", "cancelledAt", "cleanupStatus",
  "previousPhysicalHash", "cleanupError", "createdAt", "updatedAt", "expiresAt")
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,
  $25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35)
RETURNING `+uploadColumns, record.ID, record.LogicPath, record.PhysicalHash, record.Driver, record.ContentType,
		record.UploadURL, record.Size, record.UploadedSize, record.Overwrite, record.ExpectedPhysicalHash,
		record.ExpectedFileID, record.ExpectedFileUpdatedAt, record.RequireAbsent, record.Status, record.Error,
		record.Revision, record.CompletionStatus,
		record.CompletionOwner, record.CompletionLeaseUntil, record.CompletionAttempts,
		record.CompletionNextAttemptAt, record.FinalizedAt, record.PublishedAt, record.CompletedAt,
		record.ObjectGeneration, record.ObjectChecksum, record.LastCompletionError, record.CancelRequestedAt,
		record.CancelledAt, record.CleanupStatus, record.PreviousPhysicalHash, record.CleanupError, record.CreatedAt, record.UpdatedAt,
		record.ExpiresAt)
	return scanUpload(row)
}

func (s *PostgresStore) FindUpload(ctx context.Context, id string) (UploadRecord, bool, error) {
	record, err := scanUpload(s.pool.QueryRow(ctx, `SELECT `+uploadColumns+` FROM "Upload" WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *PostgresStore) ListNonterminalUploads(ctx context.Context, limit int) ([]UploadRecord, error) {
	if limit <= 0 {
		return []UploadRecord{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+uploadColumns+` FROM "Upload" WHERE status IN ('pending','uploading','uploaded','finalizing','failed') ORDER BY "createdAt",id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]UploadRecord, 0, limit)
	for rows.Next() {
		record, scanErr := scanUpload(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// ListDueUploadRecoveries returns a bounded, stable-ordered snapshot of work
// that can be safely replayed. Completion work is due only after its retry
// time and lease have elapsed; cleanup work is independently replayable after
// the business completion checkpoint is durable.
func (s *PostgresStore) ListDueUploadRecoveries(ctx context.Context, now time.Time, limit int) ([]UploadRecord, error) {
	if limit <= 0 {
		return []UploadRecord{}, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+uploadColumns+` FROM "Upload"
WHERE (
  status IN ('uploaded', 'finalizing')
  AND "completionStatus" IN ('pending', 'object_ready', 'published')
  AND ("completionNextAttemptAt" IS NULL OR "completionNextAttemptAt" <= $1)
  AND ("completionLeaseUntil" IS NULL OR "completionLeaseUntil" <= $1)
) OR (
  status='complete' AND "completionStatus"='complete' AND "cleanupStatus"='pending'
)
ORDER BY "updatedAt" ASC, id ASC
LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]UploadRecord, 0, limit)
	for rows.Next() {
		record, scanErr := scanUpload(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *PostgresStore) UpdateUpload(ctx context.Context, record UploadRecord) (UploadRecord, error) {
	updated, ok, err := s.UpdateUploadConditional(ctx, record, record.Revision)
	if err != nil {
		return UploadRecord{}, err
	}
	if !ok {
		return UploadRecord{}, ErrMetadataConflict
	}
	return updated, nil
}

// UpdateUploadConditional is reserved for byte-offset and pre-completion
// reconciliation. Once durable completion or cancellation begins, callers
// must use the semantic transition methods below.
func (s *PostgresStore) UpdateUploadConditional(ctx context.Context, record UploadRecord, expectedRevision int64) (UploadRecord, bool, error) {
	now := time.Now().UTC()
	row := s.pool.QueryRow(ctx, `
UPDATE "Upload" SET "logicPath"=$2, "physicalHash"=$3, driver=$4, "contentType"=$5, "uploadUrl"=$6,
  size=$7, "uploadedSize"=$8, overwrite=$9, "expectedPhysicalHash"=$10, "expectedFileId"=$11,
  "expectedFileUpdatedAt"=$12, "requireAbsent"=$13, status=$14, error=$15, "updatedAt"=$16,
  "expiresAt"=$17, revision=revision+1
WHERE id=$1 AND revision=$18 AND "completionStatus"='none'
  AND status NOT IN ('complete', 'conflict', 'cancelling', 'cancelled', 'expired')
RETURNING `+uploadColumns, record.ID, record.LogicPath, record.PhysicalHash, record.Driver, record.ContentType,
		record.UploadURL, record.Size, record.UploadedSize, record.Overwrite, record.ExpectedPhysicalHash,
		record.ExpectedFileID, record.ExpectedFileUpdatedAt, record.RequireAbsent, record.Status, record.Error,
		now, record.ExpiresAt, expectedRevision)
	updated, err := scanUpload(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, false, nil
	}
	return updated, err == nil, err
}

func (s *PostgresStore) RequestUploadCompletion(ctx context.Context, id string, now time.Time) (UploadRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Upload" SET "completionStatus"='pending', "completionNextAttemptAt"=$2,
  "lastCompletionError"='', "updatedAt"=$2, revision=revision+1
WHERE id=$1 AND status='uploaded' AND "completionStatus" IN ('none', 'retry')
RETURNING `+uploadColumns, id, now)
	record, err := scanUpload(row)
	if err == nil {
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, false, err
	}
	record, found, err := s.FindUpload(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return UploadRecord{}, false, err
	}
	return record, record.Status != "complete" && record.CompletionStatus != "conflict" && record.Status != "cancelled", nil
}

func (s *PostgresStore) ClaimUploadCompletion(ctx context.Context, id, owner string, now, until time.Time) (UploadRecord, bool, error) {
	if strings.TrimSpace(owner) == "" || !until.After(now) {
		return UploadRecord{}, false, fmt.Errorf("valid upload completion owner and lease are required")
	}
	row := s.pool.QueryRow(ctx, `
UPDATE "Upload" SET status='finalizing', "completionOwner"=$2, "completionLeaseUntil"=$4,
  "completionAttempts"="completionAttempts"+1, "updatedAt"=$3, revision=revision+1
WHERE id=$1 AND status IN ('uploaded', 'finalizing')
  AND "completionStatus" IN ('pending', 'object_ready', 'published')
  AND ("completionNextAttemptAt" IS NULL OR "completionNextAttemptAt" <= $3)
  AND ("completionLeaseUntil" IS NULL OR "completionLeaseUntil" <= $3 OR "completionOwner"=$2)
RETURNING `+uploadColumns, id, owner, now, until)
	record, err := scanUpload(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *PostgresStore) MarkUploadObjectReady(ctx context.Context, id, owner string, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET "completionStatus"='object_ready', "finalizedAt"=COALESCE("finalizedAt", $3),
  "updatedAt"=$3, revision=revision+1
WHERE id=$1 AND "completionOwner"=$2 AND status='finalizing' AND "completionStatus" IN ('pending', 'object_ready')
RETURNING `+uploadColumns, id, owner, now))
}

func (s *PostgresStore) MarkUploadPublished(ctx context.Context, id, owner, previousPhysicalHash, cleanupStatus, cleanupError string, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET "completionStatus"='published', "publishedAt"=COALESCE("publishedAt", $3),
  "previousPhysicalHash"=$4, "cleanupStatus"=$5, "cleanupError"=$6,
  "updatedAt"=$3, revision=revision+1
WHERE id=$1 AND "completionOwner"=$2 AND status='finalizing' AND "completionStatus" IN ('object_ready', 'published')
RETURNING `+uploadColumns, id, owner, now, previousPhysicalHash, cleanupStatus, cleanupError))
}

func (s *PostgresStore) MarkUploadComplete(ctx context.Context, id, owner string, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET status='complete', error='', "completionStatus"='complete',
  "completedAt"=COALESCE("completedAt", $3), "completionOwner"=NULL, "completionLeaseUntil"=NULL,
  "completionNextAttemptAt"=NULL, "lastCompletionError"='',
  "cleanupStatus"=CASE WHEN "cleanupStatus"='none' THEN 'pending' ELSE "cleanupStatus" END,
  "updatedAt"=$3, revision=revision+1
WHERE id=$1 AND "completionOwner"=$2 AND status='finalizing' AND "completionStatus"='published'
RETURNING `+uploadColumns, id, owner, now))
}

func (s *PostgresStore) MarkUploadCleanupComplete(ctx context.Context, id string, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET "cleanupStatus"='complete', "cleanupError"='', "updatedAt"=$2, revision=revision+1
WHERE id=$1 AND status='complete' AND "completionStatus"='complete' AND "cleanupStatus"='pending'
RETURNING `+uploadColumns, id, now))
}

func (s *PostgresStore) RetryUploadCleanup(ctx context.Context, id, message string, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET "cleanupStatus"='pending', "cleanupError"=$2, "updatedAt"=$3, revision=revision+1
WHERE id=$1 AND status='complete' AND "completionStatus"='complete' AND "cleanupStatus"='pending'
RETURNING `+uploadColumns, id, message, now))
}

func (s *PostgresStore) RetryUploadCompletion(ctx context.Context, id, owner, message string, nextAttemptAt, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET status='uploaded', "completionStatus"='pending', "completionOwner"=NULL,
  "completionLeaseUntil"=NULL, "completionNextAttemptAt"=$4, "lastCompletionError"=$3,
  error=$3, "updatedAt"=$5, revision=revision+1
WHERE id=$1 AND "completionOwner"=$2 AND status='finalizing'
RETURNING `+uploadColumns, id, owner, message, nextAttemptAt, now))
}

func (s *PostgresStore) MarkUploadCompletionConflict(ctx context.Context, id, owner, message string, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET status='conflict', "completionStatus"='conflict', "completionOwner"=NULL,
  "completionLeaseUntil"=NULL, "completionNextAttemptAt"=NULL, "lastCompletionError"=$3,
  error=$3, "updatedAt"=$4, revision=revision+1
WHERE id=$1 AND "completionOwner"=$2 AND status='finalizing' AND "completionStatus" <> 'complete'
RETURNING `+uploadColumns, id, owner, message, now))
}

func (s *PostgresStore) RequestUploadCancel(ctx context.Context, id string, now time.Time) (UploadRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Upload" SET status='cancelling', "completionStatus"='cancel_requested',
  "cancelRequestedAt"=COALESCE("cancelRequestedAt", $2), "updatedAt"=$2, revision=revision+1
WHERE id=$1 AND status IN ('pending', 'uploading', 'uploaded', 'failed', 'expired')
  AND "completionStatus" IN ('none', 'pending') AND "completionOwner" IS NULL
RETURNING `+uploadColumns, id, now)
	record, err := scanUpload(row)
	if err == nil {
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, false, err
	}
	record, found, err := s.FindUpload(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return UploadRecord{}, false, err
	}
	return record, record.Status == "cancelling", nil
}

func (s *PostgresStore) MarkUploadCancelled(ctx context.Context, id string, now time.Time) (UploadRecord, error) {
	return s.scanExpectedUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET status='cancelled', "completionStatus"='cancelled', error='',
  "cancelledAt"=COALESCE("cancelledAt", $2), "completionOwner"=NULL, "completionLeaseUntil"=NULL,
  "updatedAt"=$2, revision=revision+1
WHERE id=$1 AND status='cancelling' AND "completionStatus"='cancel_requested'
RETURNING `+uploadColumns, id, now))
}

func (s *PostgresStore) ExpireUpload(ctx context.Context, id string, expectedRevision int64, now time.Time) (UploadRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE "Upload" SET status='expired', error='upload session expired', "updatedAt"=$3, revision=revision+1
WHERE id=$1 AND revision=$2 AND "expiresAt" <= $3 AND status IN ('pending', 'uploading', 'uploaded', 'failed')
  AND "completionStatus"='none'
RETURNING `+uploadColumns, id, expectedRevision, now)
	record, err := scanUpload(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *PostgresStore) scanExpectedUpload(row rowScanner) (UploadRecord, error) {
	record, err := scanUpload(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, ErrMetadataConflict
	}
	return record, err
}

func (s *PostgresStore) DeleteUpload(ctx context.Context, id string) (bool, error) {
	result, err := s.pool.Exec(ctx, `DELETE FROM "Upload" WHERE id=$1`, id)
	return err == nil && result.RowsAffected() > 0, err
}
