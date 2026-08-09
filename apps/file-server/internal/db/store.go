package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound        = errors.New("file mapping not found")
	ErrDAVLockConflict = errors.New("DAV lock conflicts with an active lock")
	ErrInvalidMove     = errors.New("destination cannot be the source or its descendant")
	ErrIsDirectory     = errors.New("file mapping is a directory")
	ErrPathConflict    = errors.New("destination path already exists")
	ErrTrashBusy       = errors.New("trash is being permanently deleted")
	ErrV4Unsupported   = errors.New("operation is not yet supported by the v4 metadata namespace")
)

type FileRecord struct {
	ID            int        `json:"id"`
	LogicPath     string     `json:"logicPath"`
	PhysicalHash  string     `json:"physicalHash"`
	Size          int64      `json:"size"`
	IsDirectory   bool       `json:"isDirectory"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	TrashedAt     *time.Time `json:"trashedAt,omitempty"`
	TrashID       string     `json:"trashId,omitempty"`
	TrashRoot     bool       `json:"trashRoot,omitempty"`
	TrashDeleting bool       `json:"trashDeleting,omitempty"`
	// FolderSummary is populated for directory records returned by an indexed
	// listing. It is not part of the directory's canonical node marker.
	FolderSummary *FolderSummary `json:"folderSummary,omitempty"`
}

// FileSnapshot identifies one exact logical-file generation. PhysicalHash is
// deliberately not part of the token: rewriting the same object key is still
// a namespace mutation and must invalidate an older upload preflight.
type FileSnapshot struct {
	ID           int       `json:"id"`
	UpdatedAt    time.Time `json:"updatedAt"`
	PhysicalHash string    `json:"physicalHash,omitempty"`
}

func (r FileRecord) Snapshot() FileSnapshot {
	return FileSnapshot{ID: r.ID, UpdatedAt: r.UpdatedAt, PhysicalHash: r.PhysicalHash}
}

type DirectChildrenOptions struct {
	Query           string
	DirectoriesOnly bool
	Limit           int
	Offset          int
}

type DirectChildrenPage struct {
	Records       []FileRecord
	Total         int
	TotalBytes    int64
	FolderSummary FolderSummary
}

type ShareRecord struct {
	ID                 string     `json:"id"`
	LogicPath          string     `json:"logicPath"`
	PhysicalHash       string     `json:"physicalHash"`
	FileName           string     `json:"fileName"`
	Size               int64      `json:"size"`
	DestinationObject  string     `json:"destinationObject"`
	ShareURL           string     `json:"shareUrl"`
	Email              string     `json:"email"`
	Status             string     `json:"status"`
	Error              string     `json:"error,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
	NotifiedAt         *time.Time `json:"notifiedAt,omitempty"`
	ProcessingBy       *string    `json:"processingBy,omitempty"`
	ProcessingUntil    *time.Time `json:"processingUntil,omitempty"`
	DispatchStatus     string     `json:"dispatchStatus"`
	DispatchAttempts   int        `json:"dispatchAttempts"`
	NextDispatchAt     *time.Time `json:"nextDispatchAt,omitempty"`
	DispatchLeaseOwner *string    `json:"dispatchLeaseOwner,omitempty"`
	DispatchLeaseUntil *time.Time `json:"dispatchLeaseUntil,omitempty"`
	LastDispatchError  string     `json:"lastDispatchError,omitempty"`
	StartRequestedAt   *time.Time `json:"startRequestedAt,omitempty"`
}

type DAVLockRecord struct {
	Token     string     `json:"token"`
	Path      string     `json:"path"`
	Owner     string     `json:"owner"`
	Depth     int        `json:"depth"`
	ExpiresAt time.Time  `json:"expiresAt"`
	CreatedAt time.Time  `json:"createdAt"`
	HeldBy    *string    `json:"heldBy,omitempty"`
	HeldUntil *time.Time `json:"heldUntil,omitempty"`
}

type PostgresStore struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// NewPostgres is the explicit PostgreSQL constructor. New remains available
// for backwards compatibility.
func NewPostgres(ctx context.Context, databaseURL string) (Store, error) {
	return New(ctx, databaseURL)
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS "File" (
  id SERIAL PRIMARY KEY,
  "logicPath" TEXT NOT NULL,
  "physicalHash" TEXT NOT NULL,
  size BIGINT NOT NULL DEFAULT 0,
  "isDirectory" BOOLEAN NOT NULL DEFAULT false,
  "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE "File" ADD COLUMN IF NOT EXISTS "trashedAt" TIMESTAMPTZ;
ALTER TABLE "File" ADD COLUMN IF NOT EXISTS "trashId" TEXT;
ALTER TABLE "File" ADD COLUMN IF NOT EXISTS "trashRoot" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "File" ADD COLUMN IF NOT EXISTS "trashDeleting" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "File" DROP CONSTRAINT IF EXISTS "File_logicPath_key";
CREATE UNIQUE INDEX IF NOT EXISTS "File_active_logicPath_uidx" ON "File" ("logicPath") WHERE "trashedAt" IS NULL;
CREATE INDEX IF NOT EXISTS "File_logicPath_idx" ON "File" ("logicPath");
CREATE INDEX IF NOT EXISTS "File_active_logicPath_pattern_idx" ON "File" ("logicPath" text_pattern_ops) WHERE "trashedAt" IS NULL;
CREATE INDEX IF NOT EXISTS "File_trashId_idx" ON "File" ("trashId") WHERE "trashedAt" IS NOT NULL;

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
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "processingBy" TEXT;
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "processingUntil" TIMESTAMPTZ;
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "dispatchStatus" TEXT NOT NULL DEFAULT 'none';
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "dispatchAttempts" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "nextDispatchAt" TIMESTAMPTZ;
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "dispatchLeaseOwner" TEXT;
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "dispatchLeaseUntil" TIMESTAMPTZ;
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "lastDispatchError" TEXT NOT NULL DEFAULT '';
ALTER TABLE "Share" ADD COLUMN IF NOT EXISTS "startRequestedAt" TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS "Share_logicPath_idx" ON "Share" ("logicPath");
CREATE INDEX IF NOT EXISTS "Share_status_idx" ON "Share" (status);
CREATE INDEX IF NOT EXISTS "Share_dispatch_due_idx" ON "Share" ("nextDispatchAt", "dispatchStatus") WHERE status <> 'notified';

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

CREATE TABLE IF NOT EXISTS "Upload" (
  id TEXT PRIMARY KEY,
  "logicPath" TEXT NOT NULL,
  "physicalHash" TEXT NOT NULL,
  driver TEXT NOT NULL DEFAULT '',
  "contentType" TEXT NOT NULL DEFAULT '',
  "uploadUrl" TEXT NOT NULL DEFAULT '',
  size BIGINT NOT NULL DEFAULT 0,
  "uploadedSize" BIGINT NOT NULL DEFAULT 0,
  overwrite BOOLEAN NOT NULL DEFAULT false,
  "expectedPhysicalHash" TEXT,
  "expectedFileId" INTEGER NOT NULL DEFAULT 0,
  "expectedFileUpdatedAt" TIMESTAMPTZ,
  "requireAbsent" BOOLEAN NOT NULL DEFAULT false,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  revision BIGINT NOT NULL DEFAULT 1,
  "completionStatus" TEXT NOT NULL DEFAULT 'none',
  "completionOwner" TEXT,
  "completionLeaseUntil" TIMESTAMPTZ,
  "completionAttempts" INTEGER NOT NULL DEFAULT 0,
  "completionNextAttemptAt" TIMESTAMPTZ,
  "finalizedAt" TIMESTAMPTZ,
  "publishedAt" TIMESTAMPTZ,
  "completedAt" TIMESTAMPTZ,
  "objectGeneration" BIGINT NOT NULL DEFAULT 0,
  "objectChecksum" TEXT NOT NULL DEFAULT '',
  "lastCompletionError" TEXT NOT NULL DEFAULT '',
  "cancelRequestedAt" TIMESTAMPTZ,
  "cancelledAt" TIMESTAMPTZ,
  "cleanupStatus" TEXT NOT NULL DEFAULT 'none',
  "previousPhysicalHash" TEXT NOT NULL DEFAULT '',
  "cleanupError" TEXT NOT NULL DEFAULT '',
  "createdAt" TIMESTAMPTZ NOT NULL,
  "updatedAt" TIMESTAMPTZ NOT NULL,
  "expiresAt" TIMESTAMPTZ NOT NULL
);
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "uploadUrl" TEXT NOT NULL DEFAULT '';
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "expectedFileId" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "expectedFileUpdatedAt" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "completionStatus" TEXT NOT NULL DEFAULT 'none';
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "completionOwner" TEXT;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "completionLeaseUntil" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "completionAttempts" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "completionNextAttemptAt" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "finalizedAt" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "publishedAt" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "completedAt" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "objectGeneration" BIGINT NOT NULL DEFAULT 0;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "objectChecksum" TEXT NOT NULL DEFAULT '';
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "lastCompletionError" TEXT NOT NULL DEFAULT '';
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "cancelRequestedAt" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "cancelledAt" TIMESTAMPTZ;
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "cleanupStatus" TEXT NOT NULL DEFAULT 'none';
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "previousPhysicalHash" TEXT NOT NULL DEFAULT '';
ALTER TABLE "Upload" ADD COLUMN IF NOT EXISTS "cleanupError" TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS "Upload_expiresAt_idx" ON "Upload" ("expiresAt");
CREATE INDEX IF NOT EXISTS "Upload_completion_due_idx" ON "Upload" ("completionNextAttemptAt", "completionStatus")
  WHERE "completionStatus" IN ('pending', 'retry');

CREATE TABLE IF NOT EXISTS "Thumbnail" (
  id TEXT PRIMARY KEY,
  "physicalHash" TEXT NOT NULL,
  "contentType" TEXT NOT NULL,
  size BIGINT NOT NULL,
  width INTEGER NOT NULL,
  height INTEGER NOT NULL,
  "createdAt" TIMESTAMPTZ NOT NULL,
  "deleteAfter" TIMESTAMPTZ
);
ALTER TABLE "Thumbnail" ADD COLUMN IF NOT EXISTS "deleteAfter" TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS "Thumbnail_deleteAfter_idx" ON "Thumbnail" ("deleteAfter") WHERE "deleteAfter" IS NOT NULL;
CREATE TABLE IF NOT EXISTS "FileThumbnail" (
  "fileId" INTEGER PRIMARY KEY REFERENCES "File"(id) ON DELETE CASCADE,
  "thumbnailId" TEXT NOT NULL REFERENCES "Thumbnail"(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS "FileThumbnail_thumbnailId_idx" ON "FileThumbnail" ("thumbnailId");

CREATE TABLE IF NOT EXISTS "DriftPlan" (
  id TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL,
  payload JSONB NOT NULL,
  "createdAt" TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS "DriftPlan_fingerprint_uidx" ON "DriftPlan" (fingerprint);

CREATE TABLE IF NOT EXISTS "DriftSnapshot" (
  id SMALLINT PRIMARY KEY CHECK (id = 1),
  payload JSONB NOT NULL,
  "updatedAt" TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS "DriftScan" (
  id SMALLINT PRIMARY KEY CHECK (id = 1),
  "scanId" TEXT NOT NULL,
  status TEXT NOT NULL,
  phase TEXT NOT NULL,
  payload JSONB NOT NULL,
  version BIGINT NOT NULL DEFAULT 1,
  "createdAt" TIMESTAMPTZ NOT NULL,
  "updatedAt" TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS "DriftAction" (
  id TEXT PRIMARY KEY,
  "planId" TEXT NOT NULL REFERENCES "DriftPlan"(id),
  "idempotencyKey" TEXT NOT NULL,
  status TEXT NOT NULL,
  checkpoint TEXT NOT NULL,
  payload JSONB NOT NULL,
  version BIGINT NOT NULL DEFAULT 1,
  "createdAt" TIMESTAMPTZ NOT NULL,
  "updatedAt" TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS "DriftAction_planId_idx" ON "DriftAction" ("planId");

CREATE TABLE IF NOT EXISTS "DriftActionDismissal" (
  "actionId" TEXT PRIMARY KEY REFERENCES "DriftAction"(id) ON DELETE CASCADE,
  "dismissedAt" TIMESTAMPTZ NOT NULL
);
`)
	return err
}
