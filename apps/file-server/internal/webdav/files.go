package webdav

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

var errUnsupported = errors.New("operation is not supported by the WebDAV filesystem")

type directoryFile struct {
	name    string
	info    fileInfo
	entries []os.FileInfo
	offset  int
}

func (f *directoryFile) Close() error                   { return nil }
func (f *directoryFile) Read([]byte) (int, error)       { return 0, errUnsupported }
func (f *directoryFile) Seek(int64, int) (int64, error) { return 0, errUnsupported }
func (f *directoryFile) Write([]byte) (int, error)      { return 0, errUnsupported }
func (f *directoryFile) Stat() (os.FileInfo, error)     { return f.info, nil }
func (f *directoryFile) Readdir(count int) ([]os.FileInfo, error) {
	if count <= 0 {
		remaining := f.entries[f.offset:]
		f.offset = len(f.entries)
		return remaining, nil
	}
	if f.offset >= len(f.entries) {
		return nil, io.EOF
	}
	end := f.offset + count
	if end > len(f.entries) {
		end = len(f.entries)
	}
	result := f.entries[f.offset:end]
	f.offset = end
	return result, nil
}

type readFile struct {
	ctx     context.Context
	objects blob.Store
	record  db.FileRecord
	info    fileInfo
	reader  io.ReadCloser
	offset  int64
	closed  bool
}

func (f *readFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.reader == nil {
		reader, err := f.objects.NewRangeReader(f.ctx, f.record.PhysicalHash, f.offset, -1)
		if err != nil {
			return 0, err
		}
		f.reader = reader
	}
	n, err := f.reader.Read(p)
	f.offset += int64(n)
	return n, err
}

func (f *readFile) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = f.record.Size + offset
	default:
		return 0, errors.New("invalid seek origin")
	}
	if next < 0 {
		return 0, errors.New("negative seek offset")
	}
	if f.reader != nil {
		if err := f.reader.Close(); err != nil {
			return 0, err
		}
		f.reader = nil
	}
	f.offset = next
	return next, nil
}

func (f *readFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.reader != nil {
		return f.reader.Close()
	}
	return nil
}

func (f *readFile) Write([]byte) (int, error)          { return 0, errUnsupported }
func (f *readFile) Readdir(int) ([]os.FileInfo, error) { return nil, errUnsupported }
func (f *readFile) Stat() (os.FileInfo, error)         { return f.info, nil }

type uploadFile struct {
	ctx                  context.Context
	commands             commandService
	logicPath            string
	physicalHash         string
	expectedPhysicalHash *string
	requireAbsent        bool
	writer               io.WriteCloser
	size                 int64
	writeErr             error
	mu                   sync.Mutex
	closed               bool
}

type uploadSnapshot interface {
	Find(context.Context, string) (db.FileRecord, bool, error)
}

func newUploadFile(ctx context.Context, store uploadSnapshot, commands commandService, objects blob.Store, logicPath string) (*uploadFile, error) {
	physicalHash, err := objectkey.FromLogicalPath(logicPath)
	if err != nil {
		return nil, err
	}
	existing, found, err := store.Find(ctx, logicPath)
	if err != nil {
		return nil, err
	}
	if found && existing.IsDirectory {
		return nil, os.ErrExist
	}
	var expected *string
	if found {
		value := existing.PhysicalHash
		expected = &value
	}
	writer, err := blob.NewUploadWriter(ctx, objects, physicalHash, expected)
	if err != nil {
		return nil, err
	}
	return &uploadFile{
		ctx: ctx, commands: commands, logicPath: logicPath,
		physicalHash: physicalHash, expectedPhysicalHash: expected,
		requireAbsent: !found, writer: writer,
	}, nil
}

func (f *uploadFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	n, err := f.writer.Write(p)
	f.size += int64(n)
	if err != nil {
		f.writeErr = err
	}
	return n, err
}

func (f *uploadFile) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	writeErr := f.writeErr
	f.mu.Unlock()

	if writeErr != nil || f.ctx.Err() != nil {
		cause := errors.Join(writeErr, f.ctx.Err())
		abortUploadWriter(f.writer, cause)
		return cause
	}
	if closeErr := f.writer.Close(); closeErr != nil {
		return closeErr
	}
	if session := uploadSessionFromContext(f.ctx); session != nil {
		session.add(f)
		return nil
	}
	return f.commit()
}

func (f *uploadFile) commit() error {
	intent := fileops.PublishIntent{
		LogicPath:            f.logicPath,
		PhysicalHash:         f.physicalHash,
		Size:                 f.size,
		ExpectedPhysicalHash: f.expectedPhysicalHash,
		RequireAbsent:        f.requireAbsent,
	}
	if condition := writeConditionFromContext(f.ctx); condition != nil && condition.path == f.logicPath {
		intent.ExpectedPhysicalHash = condition.expectedPhysicalHash
		intent.RequireAbsent = condition.requireAbsent
	}
	_, err := f.commands.PublishUploaded(f.ctx, intent)
	if err != nil {
		if errors.Is(err, db.ErrPathConflict) {
			return errPreconditionFailed
		}
		return err
	}
	return nil
}

func (f *uploadFile) abort() {
	// The writer has already published to the final key by the time a WebDAV
	// transaction commits. Deleting here could remove a concurrent/aligned
	// object, so metadata failure leaves the object for retry/reconciliation.
}

func abortUploadWriter(writer io.WriteCloser, cause error) {
	if abortable, ok := writer.(blob.AbortableWriter); ok {
		_ = abortable.CloseWithError(cause)
		return
	}
	_ = writer.Close()
}

func (f *uploadFile) Read([]byte) (int, error)           { return 0, errUnsupported }
func (f *uploadFile) Seek(int64, int) (int64, error)     { return 0, errUnsupported }
func (f *uploadFile) Readdir(int) ([]os.FileInfo, error) { return nil, errUnsupported }
func (f *uploadFile) Stat() (os.FileInfo, error) {
	return fileInfo{
		name: pathBase(f.logicPath), size: f.size, updatedAt: nowUTC(), physicalHash: f.physicalHash,
	}, nil
}

func pathBase(value string) string {
	for len(value) > 1 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] == '/' {
			return value[i+1:]
		}
	}
	return value
}

func nowUTC() time.Time { return time.Now().UTC() }
