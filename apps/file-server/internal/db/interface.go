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
	ReplaceThumbnail(context.Context, ThumbnailRecord, []int) ([]ThumbnailRecord, error)
	FindThumbnail(context.Context, string) (ThumbnailRecord, bool, error)
	FindThumbnailsForFiles(context.Context, []int) (map[int]ThumbnailRecord, error)
	DetachThumbnails(context.Context, []int) ([]ThumbnailRecord, error)

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

type ThumbnailRecord struct {
	ID           string     `json:"id"`
	PhysicalHash string     `json:"physicalHash"`
	ContentType  string     `json:"contentType"`
	Size         int64      `json:"size"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	CreatedAt    time.Time  `json:"createdAt"`
	DeleteAfter  *time.Time `json:"deleteAfter,omitempty"`
}

// FileThumbnailLink is the canonical TreeStore lookup from a logical file to
// its thumbnail. TreeStore reads use this direct entity instead of scanning
// every thumbnail record.
//
// A link is only written after its ThumbnailRecord has been persisted. This
// ordering means an interrupted write can leave an unreferenced thumbnail, but
// never a file pointing at a thumbnail record that does not exist.
type FileThumbnailLink struct {
	FileID      int       `json:"fileId"`
	ThumbnailID string    `json:"thumbnailId"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ThumbnailGarbageCollector is implemented by metadata stores that defer
// thumbnail object deletion. Callers must delete the returned physical objects
// only after this method has successfully removed their metadata records.
type ThumbnailGarbageCollector interface {
	CleanupExpiredThumbnails(context.Context, time.Time, func(context.Context, ThumbnailRecord) error) (int, error)
}

// MetadataStats is a cheap logical metadata summary maintained by stores that
// can avoid scanning every record. PhysicalObjects/PhysicalBytes describe the
// referenced immutable objects, not objects in the metadata bucket.
type MetadataStats struct {
	LogicalFiles        int64    `json:"logicalFiles"`
	LogicalDirs         int64    `json:"logicalDirectories"`
	LogicalBytes        int64    `json:"logicalBytes"`
	PhysicalObjects     int64    `json:"physicalObjects"`
	PhysicalBytes       int64    `json:"physicalBytes"`
	AppliedOperationIDs []string `json:"appliedOperationIds,omitempty"`
}

type MetadataStatsProvider interface {
	MetadataStats(context.Context) (MetadataStats, error)
}

// FolderSummary describes every active descendant of a directory. The
// directory itself is intentionally excluded, while nested directories are
// included in Directories.
type FolderSummary struct {
	Files       int64 `json:"files"`
	Directories int64 `json:"directories"`
	Bytes       int64 `json:"bytes"`
}

type TrashPath struct {
	Path    string `json:"path"`
	TrashID string `json:"trashId"`
}

// UploadRecord persists an upload session independently of a Cloud Run
// instance. UploadURL is an opaque GCS resumable-session capability and must
// only be returned by the create-session response, never by status responses
// or logs.
type UploadRecord struct {
	ID                   string    `json:"id"`
	LogicPath            string    `json:"logicPath"`
	PhysicalHash         string    `json:"physicalHash"`
	Driver               string    `json:"driver"`
	ContentType          string    `json:"contentType,omitempty"`
	UploadURL            string    `json:"uploadUrl,omitempty"`
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
