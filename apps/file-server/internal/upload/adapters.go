package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

// NewWithBlob wires the common local/server-proxied implementation. A GCS
// direct-upload implementation can instead call New with its own Storage.
func NewWithBlob(store MetadataStore, objects blob.Store, options ...Option) *Service {
	return NewWithBlobAndPublisher(store, storeAdapter{store: store}, objects, options...)
}

// NewWithBlobAndPublisher keeps upload-session persistence in the metadata
// adapter while delegating the final logical publication to the process-wide
// file application service.
func NewWithBlobAndPublisher(store MetadataStore, publisher Publisher, objects blob.Store, options ...Option) *Service {
	if direct, ok := objects.(blob.DirectUploadStore); ok {
		adapter := storeAdapter{store: store}
		return New(adapter, publisher, gcsDirectStorage{objects: direct}, options...)
	}
	adapter := storeAdapter{store: store}
	return New(adapter, publisher, blobStorage{objects: objects}, options...)
}

// NewWithStorage exposes the persistence adapters while allowing the caller to
// inject a GCS direct/resumable implementation.
func NewWithStorage(store MetadataStore, storage Storage, options ...Option) *Service {
	adapter := storeAdapter{store: store}
	return New(adapter, adapter, storage, options...)
}

// MetadataStore contains only the upload session and publish operations used
// by the adapters in this package.
type MetadataStore interface {
	CreateUpload(context.Context, db.UploadRecord) (db.UploadRecord, error)
	FindUpload(context.Context, string) (db.UploadRecord, bool, error)
	ListDueUploadRecoveries(context.Context, time.Time, int) ([]db.UploadRecord, error)
	UpdateUpload(context.Context, db.UploadRecord) (db.UploadRecord, error)
	UpdateUploadConditional(context.Context, db.UploadRecord, int64) (db.UploadRecord, bool, error)
	RequestUploadCompletion(context.Context, string, time.Time) (db.UploadRecord, bool, error)
	ClaimUploadCompletion(context.Context, string, string, time.Time, time.Time) (db.UploadRecord, bool, error)
	MarkUploadObjectReady(context.Context, string, string, time.Time) (db.UploadRecord, error)
	MarkUploadPublished(context.Context, string, string, string, string, string, time.Time) (db.UploadRecord, error)
	MarkUploadComplete(context.Context, string, string, time.Time) (db.UploadRecord, error)
	RetryUploadCompletion(context.Context, string, string, string, time.Time, time.Time) (db.UploadRecord, error)
	MarkUploadCompletionConflict(context.Context, string, string, string, time.Time) (db.UploadRecord, error)
	RequestUploadCancel(context.Context, string, time.Time) (db.UploadRecord, bool, error)
	MarkUploadCancelled(context.Context, string, time.Time) (db.UploadRecord, error)
	ExpireUpload(context.Context, string, int64, time.Time) (db.UploadRecord, bool, error)
	MarkUploadCleanupComplete(context.Context, string, time.Time) (db.UploadRecord, error)
	RetryUploadCleanup(context.Context, string, string, time.Time) (db.UploadRecord, error)
	DeleteUpload(context.Context, string) (bool, error)
	Find(context.Context, string) (db.FileRecord, bool, error)
	UpsertDirectory(context.Context, string) error
	ReplaceFileConditional(context.Context, string, string, int64, *string, bool) (string, bool, error)
	ReplaceFileConditionalSnapshot(context.Context, string, string, int64, *db.FileSnapshot, bool) (string, bool, error)
	IsObjectReferenced(context.Context, string, string) (bool, error)
}

type storeAdapter struct{ store MetadataStore }

func (a storeAdapter) CreateUpload(ctx context.Context, session Session) error {
	_, err := a.store.CreateUpload(ctx, toDBUpload(session))
	return err
}

func (a storeAdapter) FindUpload(ctx context.Context, id string) (Session, bool, error) {
	record, found, err := a.store.FindUpload(ctx, id)
	return fromDBUpload(record), found, err
}

func (a storeAdapter) ListDueRecoveries(ctx context.Context, now time.Time, limit int) ([]Session, error) {
	records, err := a.store.ListDueUploadRecoveries(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(records))
	for _, record := range records {
		sessions = append(sessions, fromDBUpload(record))
	}
	return sessions, nil
}

func (a storeAdapter) UpdateUpload(ctx context.Context, session Session, expectedRevision int64) (Session, bool, error) {
	record, updated, err := a.store.UpdateUploadConditional(ctx, toDBUpload(session), expectedRevision)
	return fromDBUpload(record), updated, err
}

func (a storeAdapter) RequestCompletion(ctx context.Context, id string, now time.Time) (Session, bool, error) {
	record, needed, err := a.store.RequestUploadCompletion(ctx, id, now)
	return fromDBUpload(record), needed, err
}

func (a storeAdapter) ClaimCompletion(ctx context.Context, id, owner string, now, until time.Time) (Session, bool, error) {
	record, claimed, err := a.store.ClaimUploadCompletion(ctx, id, owner, now, until)
	return fromDBUpload(record), claimed, err
}

func (a storeAdapter) MarkObjectReady(ctx context.Context, id, owner string, now time.Time) (Session, error) {
	record, err := a.store.MarkUploadObjectReady(ctx, id, owner, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) MarkPublished(ctx context.Context, id, owner, previous, cleanupStatus, cleanupError string, now time.Time) (Session, error) {
	record, err := a.store.MarkUploadPublished(ctx, id, owner, previous, cleanupStatus, cleanupError, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) MarkComplete(ctx context.Context, id, owner string, now time.Time) (Session, error) {
	record, err := a.store.MarkUploadComplete(ctx, id, owner, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) RetryCompletion(ctx context.Context, id, owner, message string, next, now time.Time) (Session, error) {
	record, err := a.store.RetryUploadCompletion(ctx, id, owner, message, next, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) MarkCompletionConflict(ctx context.Context, id, owner, message string, now time.Time) (Session, error) {
	record, err := a.store.MarkUploadCompletionConflict(ctx, id, owner, message, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) RequestCancel(ctx context.Context, id string, now time.Time) (Session, bool, error) {
	record, needed, err := a.store.RequestUploadCancel(ctx, id, now)
	return fromDBUpload(record), needed, err
}

func (a storeAdapter) MarkCancelled(ctx context.Context, id string, now time.Time) (Session, error) {
	record, err := a.store.MarkUploadCancelled(ctx, id, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) ExpireUpload(ctx context.Context, id string, expectedRevision int64, now time.Time) (Session, bool, error) {
	record, expired, err := a.store.ExpireUpload(ctx, id, expectedRevision, now)
	return fromDBUpload(record), expired, err
}

func (a storeAdapter) MarkCleanupComplete(ctx context.Context, id string, now time.Time) (Session, error) {
	record, err := a.store.MarkUploadCleanupComplete(ctx, id, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) RetryCleanup(ctx context.Context, id, message string, now time.Time) (Session, error) {
	record, err := a.store.RetryUploadCleanup(ctx, id, message, now)
	return fromDBUpload(record), err
}

func (a storeAdapter) FindFile(ctx context.Context, logicPath string) (File, bool, error) {
	record, found, err := a.store.Find(ctx, logicPath)
	return File{ID: record.ID, PhysicalHash: record.PhysicalHash, IsDirectory: record.IsDirectory, Size: record.Size, UpdatedAt: record.UpdatedAt}, found, err
}

func (a storeAdapter) EnsureDirectory(ctx context.Context, logicPath string) error {
	record, found, err := a.store.Find(ctx, logicPath)
	if err != nil {
		return err
	}
	if found {
		if !record.IsDirectory {
			return fmt.Errorf("upload parent path is a file: %s", logicPath)
		}
		return nil
	}
	return a.store.UpsertDirectory(ctx, logicPath)
}

func (a storeAdapter) ReplaceFile(ctx context.Context, logicPath, physicalHash string, size int64, expected *string, snapshot *FileSnapshot, absent bool) (PublishResult, bool, error) {
	var previous string
	var matched bool
	var err error
	if snapshot != nil {
		previous, matched, err = a.store.ReplaceFileConditionalSnapshot(ctx, logicPath, physicalHash, size, &db.FileSnapshot{
			ID: snapshot.ID, UpdatedAt: snapshot.UpdatedAt, PhysicalHash: snapshot.PhysicalHash,
		}, absent)
	} else {
		previous, matched, err = a.store.ReplaceFileConditional(ctx, logicPath, physicalHash, size, expected, absent)
	}
	result := PublishResult{PreviousPhysicalHash: previous}
	result.CleanupPending = matched && previous != "" && previous != physicalHash
	return result, matched, err
}

func (a storeAdapter) IsUploadGenerationReferenced(ctx context.Context, physicalHash string) (bool, error) {
	return a.store.IsObjectReferenced(ctx, physicalHash, "")
}

func toDBUpload(session Session) db.UploadRecord {
	record := db.UploadRecord{
		ID: session.ID, LogicPath: session.LogicPath, PhysicalHash: session.PhysicalHash,
		Driver: session.Driver, ContentType: session.ContentType, Size: session.Size, Overwrite: session.Overwrite,
		UploadURL:            session.UploadURL,
		UploadedSize:         session.UploadedSize,
		ExpectedPhysicalHash: session.ExpectedPhysicalHash, RequireAbsent: session.RequireAbsent,
		ExpectedFileID: session.ExpectedFileID, ExpectedFileUpdatedAt: session.ExpectedFileUpdatedAt,
		Status: session.Status, Error: session.Error, CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt, ExpiresAt: session.ExpiresAt,
		Revision: session.Revision, CompletionStatus: session.CompletionStatus,
		CompletionLeaseUntil: session.CompletionLeaseUntil, CompletionAttempts: session.CompletionAttempts,
		CompletionNextAttemptAt: session.CompletionNextAttemptAt,
		FinalizedAt:             session.FinalizedAt, PublishedAt: session.PublishedAt, CompletedAt: session.CompletedAt,
		LastCompletionError: session.LastCompletionError, CancelRequestedAt: session.CancelRequestedAt,
		CancelledAt: session.CancelledAt, PreviousPhysicalHash: session.PreviousPhysicalHash,
		CleanupStatus: session.CleanupStatus, CleanupError: session.CleanupError,
	}
	if strings.TrimSpace(session.CompletionOwner) != "" {
		record.CompletionOwner = &session.CompletionOwner
	}
	return record
}

func fromDBUpload(record db.UploadRecord) Session {
	session := Session{
		ID: record.ID, LogicPath: record.LogicPath, PhysicalHash: record.PhysicalHash,
		Driver: record.Driver, ContentType: record.ContentType, Size: record.Size, Overwrite: record.Overwrite,
		UploadURL:            record.UploadURL,
		UploadedSize:         record.UploadedSize,
		ExpectedPhysicalHash: record.ExpectedPhysicalHash, RequireAbsent: record.RequireAbsent,
		ExpectedFileID: record.ExpectedFileID, ExpectedFileUpdatedAt: record.ExpectedFileUpdatedAt,
		Status: record.Status, Error: record.Error, CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt,
		Revision: record.Revision, CompletionStatus: record.CompletionStatus,
		CompletionLeaseUntil: record.CompletionLeaseUntil, CompletionAttempts: record.CompletionAttempts,
		CompletionNextAttemptAt: record.CompletionNextAttemptAt,
		FinalizedAt:             record.FinalizedAt, PublishedAt: record.PublishedAt, CompletedAt: record.CompletedAt,
		LastCompletionError: record.LastCompletionError, CancelRequestedAt: record.CancelRequestedAt,
		CancelledAt: record.CancelledAt, PreviousPhysicalHash: record.PreviousPhysicalHash,
		CleanupStatus: record.CleanupStatus, CleanupError: record.CleanupError,
	}
	if record.CompletionOwner != nil {
		session.CompletionOwner = *record.CompletionOwner
	}
	return session
}

type blobStorage struct{ objects blob.Store }

func (s blobStorage) Driver() string { return s.objects.Driver() }

func (s blobStorage) Prepare(context.Context, Session) (PreparedTarget, error) {
	return PreparedTarget{}, nil
}

func (s blobStorage) Write(ctx context.Context, session Session, source io.Reader) (int64, error) {
	writer, err := blob.NewUploadWriter(ctx, s.objects, session.PhysicalHash, nil)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(writer, source)
	if copyErr != nil {
		if abortable, ok := writer.(blob.AbortableWriter); ok {
			_ = abortable.CloseWithError(copyErr)
		} else {
			_ = writer.Close()
		}
		return written, copyErr
	}
	if written != session.Size {
		sizeErr := fmt.Errorf("uploaded size %d does not match declared size %d", written, session.Size)
		if abortable, ok := writer.(blob.AbortableWriter); ok {
			_ = abortable.CloseWithError(sizeErr)
		} else {
			_ = writer.Close()
		}
		return written, sizeErr
	}
	closeErr := writer.Close()
	if closeErr != nil {
		return written, closeErr
	}
	return written, nil
}

func (s blobStorage) WriteChunk(ctx context.Context, session Session, offset int64, source io.Reader) (int64, error) {
	resumable, ok := s.objects.(blob.LocalResumableUploadStore)
	if !ok {
		return 0, errors.New("local object store does not support resumable uploads")
	}
	return resumable.WriteUploadChunk(ctx, session.ID, offset, source)
}

func (s blobStorage) RollbackChunk(ctx context.Context, session Session, offset int64) error {
	resumable, ok := s.objects.(blob.LocalResumableUploadStore)
	if !ok {
		return errors.New("local object store does not support resumable uploads")
	}
	return resumable.TruncateUpload(ctx, session.ID, offset)
}

func (s blobStorage) Offset(ctx context.Context, session Session) (int64, bool, error) {
	resumable, ok := s.objects.(blob.LocalResumableUploadStore)
	if !ok {
		return 0, false, errors.New("local object store does not support resumable uploads")
	}
	offset, exists, err := resumable.UploadOffset(ctx, session.ID)
	return offset, exists && offset == session.Size, err
}

func (s blobStorage) Finalize(ctx context.Context, session Session) (int64, error) {
	resumable, ok := s.objects.(blob.LocalResumableUploadStore)
	if !ok {
		return 0, errors.New("local object store does not support resumable uploads")
	}
	// Upload object keys are immutable per session, so the final target must
	// always be created as absent. ExpectedPhysicalHash belongs to namespace
	// publication and must not authorize an in-place blob overwrite.
	if err := resumable.CompleteUpload(ctx, session.ID, session.PhysicalHash, session.Size, nil); err != nil {
		return 0, err
	}
	return session.Size, nil
}

func (s blobStorage) Stat(ctx context.Context, physicalHash string) (int64, error) {
	objects, err := s.objects.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, object := range objects {
		if object.Name == physicalHash {
			return object.Size, nil
		}
	}
	return 0, fmt.Errorf("uploaded object %q not found", physicalHash)
}

func (s blobStorage) Delete(ctx context.Context, physicalHash string) error {
	return s.objects.Delete(ctx, physicalHash)
}

func (s blobStorage) Cancel(ctx context.Context, session Session) error {
	if resumable, ok := s.objects.(blob.LocalResumableUploadStore); ok {
		return resumable.CancelUpload(ctx, session.ID)
	}
	return nil
}

type gcsDirectStorage struct{ objects blob.DirectUploadStore }

func (s gcsDirectStorage) Driver() string { return s.objects.Driver() }

func (s gcsDirectStorage) Prepare(ctx context.Context, session Session) (PreparedTarget, error) {
	// The session-specific object key is immutable. generation-match=0 makes a
	// duplicate/colliding object explicit instead of overwriting bytes before
	// the logical-path CAS has won.
	url, headers, err := s.objects.StartResumableUpload(ctx, session.PhysicalHash, session.ContentType, session.UploadOrigin, session.Size, 0)
	return PreparedTarget{URL: url, Headers: headers}, err
}

func (s gcsDirectStorage) Write(context.Context, Session, io.Reader) (int64, error) {
	return 0, errors.New("GCS direct uploads must use the resumable upload URL")
}

func (s gcsDirectStorage) WriteChunk(context.Context, Session, int64, io.Reader) (int64, error) {
	return 0, errors.New("GCS direct uploads must use the resumable upload URL")
}

func (s gcsDirectStorage) RollbackChunk(context.Context, Session, int64) error {
	return errors.New("GCS direct uploads must use the resumable upload URL")
}

func (s gcsDirectStorage) Offset(ctx context.Context, session Session) (int64, bool, error) {
	if strings.TrimSpace(session.UploadURL) == "" {
		return 0, false, ErrInvalidSession
	}
	offset, complete, err := s.objects.QueryResumableUpload(ctx, session.UploadURL, session.Size)
	if errors.Is(err, blob.ErrResumableUploadGone) {
		return 0, false, errors.Join(ErrResumableSessionGone, err)
	}
	return offset, complete, err
}

func (s gcsDirectStorage) Finalize(ctx context.Context, session Session) (int64, error) {
	return s.Stat(ctx, session.PhysicalHash)
}

func (s gcsDirectStorage) Stat(ctx context.Context, physicalHash string) (int64, error) {
	object, err := s.objects.StatObject(ctx, physicalHash)
	return object.Size, err
}

func (s gcsDirectStorage) Delete(ctx context.Context, physicalHash string) error {
	return s.objects.Delete(ctx, physicalHash)
}

func (s gcsDirectStorage) Cancel(ctx context.Context, session Session) error {
	if strings.TrimSpace(session.UploadURL) == "" {
		return ErrCancellationUnavailable
	}
	return s.objects.CancelResumableUpload(ctx, session.UploadURL)
}
