package db

import (
	"context"
	"time"
)

// Store is the metadata persistence contract used by the file server. Both
// PostgreSQL and JSON backends provide the same atomic operations.
type Store interface {
	Close()
	EnsureSchema(context.Context) error

	CreateDAVLock(context.Context, DAVLockRecord) (DAVLockRecord, error)
	FindDAVLock(context.Context, string) (DAVLockRecord, bool, error)
	ListActiveDAVLocks(context.Context, string) ([]DAVLockRecord, error)
	RefreshDAVLock(context.Context, string, time.Time) (DAVLockRecord, bool, error)
	DeleteDAVLock(context.Context, string) (bool, error)
	CleanupExpiredDAVLocks(context.Context) (int64, error)
	ClaimDAVLocks(context.Context, []string, []string, string, time.Time) (bool, error)
	ReleaseDAVLockClaim(context.Context, string) error

	Find(context.Context, string) (FileRecord, bool, error)
	ListPrefix(context.Context, string) ([]FileRecord, error)
	ListAll(context.Context) ([]FileRecord, error)
	ListDirectChildren(context.Context, string, DirectChildrenOptions) (DirectChildrenPage, error)
	UpsertFile(context.Context, string, string, int64) error
	ReplaceFile(context.Context, string, string, int64) (string, error)
	ReplaceFileConditional(context.Context, string, string, int64, *string, bool) (string, bool, error)
	UpsertDirectory(context.Context, string) error
	RenamePath(context.Context, string, string) error
	DeletePath(context.Context, string) error
	DeletePrefix(context.Context, string) error
	BatchMove(context.Context, []string, string) ([]FileRecord, error)
	TrashPaths(context.Context, []TrashPath) ([]FileRecord, error)
	ListTrash(context.Context) ([]FileRecord, error)
	RestoreTrash(context.Context, []string) ([]FileRecord, error)
	ListTrashRecords(context.Context, []string) ([]FileRecord, error)
	ClaimTrash(context.Context, []string) ([]FileRecord, error)
	DeleteTrash(context.Context, []string) (int64, error)

	CreateShare(context.Context, ShareRecord) (ShareRecord, error)
	FindShare(context.Context, string) (ShareRecord, bool, error)
	MarkShareUploading(context.Context, string, string) (ShareRecord, error)
	MarkShareUploaded(context.Context, string) (ShareRecord, error)
	MarkShareNotified(context.Context, string) (ShareRecord, error)
	MarkShareFailed(context.Context, string, string, string) (ShareRecord, error)
	ClaimShareJob(context.Context, string, string, time.Time) (ShareRecord, bool, error)
	ReleaseShareJob(context.Context, string, string) error

	CreateUpload(context.Context, UploadRecord) (UploadRecord, error)
	FindUpload(context.Context, string) (UploadRecord, bool, error)
	UpdateUpload(context.Context, UploadRecord) (UploadRecord, error)
	DeleteUpload(context.Context, string) (bool, error)
}

type TrashPath struct {
	Path    string `json:"path"`
	TrashID string `json:"trashId"`
}

// UploadRecord persists an upload session independently of a Cloud Run
// instance. PhysicalHash is the provisional object key until completion.
type UploadRecord struct {
	ID                   string    `json:"id"`
	LogicPath            string    `json:"logicPath"`
	PhysicalHash         string    `json:"physicalHash"`
	Driver               string    `json:"driver"`
	ContentType          string    `json:"contentType,omitempty"`
	Size                 int64     `json:"size"`
	UploadedSize         int64     `json:"uploadedSize,omitempty"`
	Overwrite            bool      `json:"overwrite,omitempty"`
	ExpectedPhysicalHash *string   `json:"expectedPhysicalHash,omitempty"`
	RequireAbsent        bool      `json:"requireAbsent,omitempty"`
	Status               string    `json:"status"`
	Error                string    `json:"error,omitempty"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
	ExpiresAt            time.Time `json:"expiresAt"`
}

var _ Store = (*PostgresStore)(nil)
