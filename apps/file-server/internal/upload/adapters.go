package upload

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

// NewWithBlob wires the common local/server-proxied implementation. A GCS
// direct-upload implementation can instead call New with its own Storage.
func NewWithBlob(store db.Store, objects blob.Store, options ...Option) *Service {
	if direct, ok := objects.(blob.DirectUploadStore); ok {
		return NewWithStorage(store, gcsDirectStorage{objects: direct}, options...)
	}
	return NewWithStorage(store, blobStorage{objects: objects}, options...)
}

// NewWithStorage exposes the persistence adapters while allowing the caller to
// inject a GCS direct/resumable implementation.
func NewWithStorage(store db.Store, storage Storage, options ...Option) *Service {
	adapter := storeAdapter{store: store}
	return New(adapter, adapter, storage, options...)
}

type storeAdapter struct{ store db.Store }

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
	return File{PhysicalHash: record.PhysicalHash, IsDirectory: record.IsDirectory}, found, err
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

func (s blobStorage) Write(ctx context.Context, physicalHash string, source io.Reader) (int64, error) {
	writer, err := s.objects.NewWriter(ctx, physicalHash)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(writer, source)
	closeErr := writer.Close()
	if copyErr != nil {
		_ = s.objects.Delete(ctx, physicalHash)
		return written, copyErr
	}
	if closeErr != nil {
		_ = s.objects.Delete(ctx, physicalHash)
		return written, closeErr
	}
	return written, nil
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

type gcsDirectStorage struct{ objects blob.DirectUploadStore }

func (s gcsDirectStorage) Driver() string { return s.objects.Driver() }

func (s gcsDirectStorage) Prepare(ctx context.Context, session Session) (PreparedTarget, error) {
	url, headers, err := s.objects.StartResumableUpload(ctx, session.PhysicalHash, session.ContentType, session.UploadOrigin, session.Size)
	return PreparedTarget{URL: url, Headers: headers}, err
}

func (s gcsDirectStorage) Write(context.Context, string, io.Reader) (int64, error) {
	return 0, errors.New("GCS direct uploads must use the resumable upload URL")
}

func (s gcsDirectStorage) Stat(ctx context.Context, physicalHash string) (int64, error) {
	object, err := s.objects.StatObject(ctx, physicalHash)
	return object.Size, err
}

func (s gcsDirectStorage) Delete(ctx context.Context, physicalHash string) error {
	return s.objects.Delete(ctx, physicalHash)
}
