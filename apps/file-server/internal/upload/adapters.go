package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

// NewWithBlob wires the common local/server-proxied implementation. A GCS
// direct-upload implementation can instead call New with its own Storage.
func NewWithBlob(store MetadataStore, objects blob.Store, options ...Option) *Service {
	if direct, ok := objects.(blob.DirectUploadStore); ok {
		return NewWithStorage(store, gcsDirectStorage{objects: direct}, options...)
	}
	return NewWithStorage(store, blobStorage{objects: objects}, options...)
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
	UpdateUpload(context.Context, db.UploadRecord) (db.UploadRecord, error)
	DeleteUpload(context.Context, string) (bool, error)
	Find(context.Context, string) (db.FileRecord, bool, error)
	UpsertDirectory(context.Context, string) error
	ReplaceFileConditional(context.Context, string, string, int64, *string, bool) (string, bool, error)
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

func (a storeAdapter) UpdateUpload(ctx context.Context, session Session) error {
	_, err := a.store.UpdateUpload(ctx, toDBUpload(session))
	return err
}

func (a storeAdapter) DeleteUpload(ctx context.Context, id string) error {
	_, err := a.store.DeleteUpload(ctx, id)
	return err
}

func (a storeAdapter) FindFile(ctx context.Context, logicPath string) (File, bool, error) {
	record, found, err := a.store.Find(ctx, logicPath)
	return File{PhysicalHash: record.PhysicalHash, IsDirectory: record.IsDirectory, Size: record.Size, UpdatedAt: record.UpdatedAt}, found, err
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

func (a storeAdapter) ReplaceFile(ctx context.Context, logicPath, physicalHash string, size int64, expected *string, absent bool) (string, bool, error) {
	return a.store.ReplaceFileConditional(ctx, logicPath, physicalHash, size, expected, absent)
}

func toDBUpload(session Session) db.UploadRecord {
	return db.UploadRecord{
		ID: session.ID, LogicPath: session.LogicPath, PhysicalHash: session.PhysicalHash,
		Driver: session.Driver, ContentType: session.ContentType, Size: session.Size, Overwrite: session.Overwrite,
		UploadURL:            session.UploadURL,
		UploadedSize:         session.UploadedSize,
		ExpectedPhysicalHash: session.ExpectedPhysicalHash, RequireAbsent: session.RequireAbsent,
		Status: session.Status, Error: session.Error, CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt, ExpiresAt: session.ExpiresAt,
	}
}

func fromDBUpload(record db.UploadRecord) Session {
	return Session{
		ID: record.ID, LogicPath: record.LogicPath, PhysicalHash: record.PhysicalHash,
		Driver: record.Driver, ContentType: record.ContentType, Size: record.Size, Overwrite: record.Overwrite,
		UploadURL:            record.UploadURL,
		UploadedSize:         record.UploadedSize,
		ExpectedPhysicalHash: record.ExpectedPhysicalHash, RequireAbsent: record.RequireAbsent,
		Status: record.Status, Error: record.Error, CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt,
	}
}

type blobStorage struct{ objects blob.Store }

func (s blobStorage) Driver() string { return s.objects.Driver() }

func (s blobStorage) Prepare(context.Context, Session) (PreparedTarget, error) {
	return PreparedTarget{}, nil
}

func (s blobStorage) Write(ctx context.Context, session Session, source io.Reader) (int64, error) {
	writer, err := blob.NewUploadWriter(ctx, s.objects, session.PhysicalHash, session.ExpectedPhysicalHash)
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
	if err := resumable.CompleteUpload(ctx, session.ID, session.PhysicalHash, session.Size, session.ExpectedPhysicalHash); err != nil {
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
	ifGenerationMatch := int64(0)
	// A logical record already using the final key is a true in-place
	// overwrite. Snapshot its generation and require that exact generation at
	// upload commit time. Legacy UUID-backed records write a previously absent
	// final key with generation-match zero.
	if session.ExpectedPhysicalHash != nil && *session.ExpectedPhysicalHash == session.PhysicalHash {
		object, err := s.objects.StatObject(ctx, session.PhysicalHash)
		if err != nil {
			return PreparedTarget{}, fmt.Errorf("stat overwrite target: %w", err)
		}
		if object.Generation <= 0 {
			return PreparedTarget{}, errors.New("overwrite target has no storage generation")
		}
		ifGenerationMatch = object.Generation
	}
	url, headers, err := s.objects.StartResumableUpload(ctx, session.PhysicalHash, session.ContentType, session.UploadOrigin, session.Size, ifGenerationMatch)
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
	return s.objects.QueryResumableUpload(ctx, session.UploadURL, session.Size)
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
