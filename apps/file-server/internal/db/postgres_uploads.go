package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateUpload(ctx context.Context, record UploadRecord) (UploadRecord, error) {
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	row := s.pool.QueryRow(ctx, `
INSERT INTO "Upload" (id, "logicPath", "physicalHash", driver, "contentType", "uploadUrl", size,
  "uploadedSize", overwrite, "expectedPhysicalHash", "requireAbsent", status, error,
  "createdAt", "updatedAt", "expiresAt")
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
RETURNING id, "logicPath", "physicalHash", driver, "contentType", "uploadUrl", size, "uploadedSize",
  overwrite, "expectedPhysicalHash", "requireAbsent", status, error, "createdAt", "updatedAt", "expiresAt"
`, record.ID, record.LogicPath, record.PhysicalHash, record.Driver, record.ContentType, record.UploadURL, record.Size,
		record.UploadedSize, record.Overwrite, record.ExpectedPhysicalHash, record.RequireAbsent,
		record.Status, record.Error, record.CreatedAt, record.UpdatedAt, record.ExpiresAt)
	return scanUpload(row)
}

func (s *PostgresStore) FindUpload(ctx context.Context, id string) (UploadRecord, bool, error) {
	record, err := scanUpload(s.pool.QueryRow(ctx, `
SELECT id, "logicPath", "physicalHash", driver, "contentType", "uploadUrl", size, "uploadedSize",
  overwrite, "expectedPhysicalHash", "requireAbsent", status, error, "createdAt", "updatedAt", "expiresAt"
FROM "Upload" WHERE id = $1
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadRecord{}, false, nil
	}
	return record, err == nil, err
}

func (s *PostgresStore) UpdateUpload(ctx context.Context, record UploadRecord) (UploadRecord, error) {
	record.UpdatedAt = time.Now().UTC()
	return scanUpload(s.pool.QueryRow(ctx, `
UPDATE "Upload" SET "logicPath"=$2, "physicalHash"=$3, driver=$4, "contentType"=$5, "uploadUrl"=$6,
  size=$7, "uploadedSize"=$8, overwrite=$9, "expectedPhysicalHash"=$10, "requireAbsent"=$11,
  status=$12, error=$13, "updatedAt"=$14, "expiresAt"=$15 WHERE id=$1
RETURNING id, "logicPath", "physicalHash", driver, "contentType", "uploadUrl", size, "uploadedSize",
  overwrite, "expectedPhysicalHash", "requireAbsent", status, error, "createdAt", "updatedAt", "expiresAt"
`, record.ID, record.LogicPath, record.PhysicalHash, record.Driver, record.ContentType, record.UploadURL, record.Size,
		record.UploadedSize, record.Overwrite, record.ExpectedPhysicalHash, record.RequireAbsent,
		record.Status, record.Error, record.UpdatedAt, record.ExpiresAt))
}

func (s *PostgresStore) DeleteUpload(ctx context.Context, id string) (bool, error) {
	result, err := s.pool.Exec(ctx, `DELETE FROM "Upload" WHERE id=$1`, id)
	return err == nil && result.RowsAffected() > 0, err
}
