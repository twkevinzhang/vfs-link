package vfs

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"cloud.google.com/go/storage"
	"github.com/spf13/afero"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/gcs"
)

var errUnsupported = errors.New("operation is not supported by database VFS")

type baseFile struct {
	name string
	info os.FileInfo
}

func (f *baseFile) Name() string {
	return f.name
}

func (f *baseFile) Stat() (os.FileInfo, error) {
	return f.info, nil
}

func (f *baseFile) Sync() error {
	return nil
}

func (f *baseFile) Truncate(size int64) error {
	return errUnsupported
}

func (f *baseFile) WriteString(s string) (int, error) {
	return f.Write([]byte(s))
}

func (f *baseFile) Read([]byte) (int, error) {
	return 0, errUnsupported
}

func (f *baseFile) ReadAt([]byte, int64) (int, error) {
	return 0, errUnsupported
}

func (f *baseFile) Seek(int64, int) (int64, error) {
	return 0, errUnsupported
}

func (f *baseFile) Write([]byte) (int, error) {
	return 0, errUnsupported
}

func (f *baseFile) WriteAt([]byte, int64) (int, error) {
	return 0, errUnsupported
}

func (f *baseFile) Readdir(int) ([]os.FileInfo, error) {
	return nil, errUnsupported
}

func (f *baseFile) Readdirnames(int) ([]string, error) {
	return nil, errUnsupported
}

func (f *baseFile) Close() error {
	return nil
}

type dirFile struct {
	baseFile
	entries []os.FileInfo
	offset  int
}

func newDirFile(name string, info os.FileInfo, entries []os.FileInfo) afero.File {
	return &dirFile{
		baseFile: baseFile{name: name, info: info},
		entries:  entries,
	}
}

func (f *dirFile) Readdir(count int) ([]os.FileInfo, error) {
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
	entries := f.entries[f.offset:end]
	f.offset = end
	if len(entries) == 0 {
		return nil, io.EOF
	}
	return entries, nil
}

func (f *dirFile) Readdirnames(count int) ([]string, error) {
	entries, err := f.Readdir(count)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

type readFile struct {
	baseFile
	reader io.ReadCloser
}

func newReadFile(name string, info os.FileInfo, reader io.ReadCloser) afero.File {
	return &readFile{
		baseFile: baseFile{name: name, info: info},
		reader:   reader,
	}
}

func (f *readFile) Read(p []byte) (int, error) {
	return f.reader.Read(p)
}

func (f *readFile) Close() error {
	return f.reader.Close()
}

type uploadFile struct {
	baseFile
	store        *db.Store
	objects      *gcs.Client
	logicPath    string
	physicalHash string
	writer       *storage.Writer
	cancel       context.CancelFunc
	size         int64
	transferErr  error
	mu           sync.Mutex
	closed       bool
}

func newUploadFile(store *db.Store, objects *gcs.Client, logicPath string) afero.File {
	ctx, cancel := context.WithCancel(context.Background())
	physicalHash := gcs.GeneratePhysicalHash(logicPath)
	return &uploadFile{
		baseFile:     baseFile{name: logicPath, info: fileInfo{name: pathBase(logicPath), mode: 0o666, modTime: now()}},
		store:        store,
		objects:      objects,
		logicPath:    logicPath,
		physicalHash: physicalHash,
		writer:       objects.NewWriter(ctx, physicalHash),
		cancel:       cancel,
	}
}

func (f *uploadFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	n, err := f.writer.Write(p)
	f.size += int64(n)
	return n, err
}

func (f *uploadFile) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	transferErr := f.transferErr
	f.mu.Unlock()

	closeErr := f.writer.Close()
	f.cancel()

	if transferErr != nil {
		_ = f.objects.Delete(context.Background(), f.physicalHash)
		return transferErr
	}
	if closeErr != nil {
		_ = f.objects.Delete(context.Background(), f.physicalHash)
		return closeErr
	}
	if err := f.store.UpsertFile(context.Background(), f.logicPath, f.physicalHash, f.size); err != nil {
		_ = f.objects.Delete(context.Background(), f.physicalHash)
		return err
	}
	return nil
}

func (f *uploadFile) TransferError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferErr = err
}
