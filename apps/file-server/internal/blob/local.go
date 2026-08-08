package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type LocalStore struct {
	root        string
	uploadMutex sync.Mutex
}

type localWriter struct {
	file      *os.File
	tempPath  string
	finalPath string
	closed    bool
}

type localRangeReader struct {
	reader io.Reader
	file   *os.File
}

func NewLocal(root string) (*LocalStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("local storage root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create local storage root: %w", err)
	}
	return &LocalStore{root: absRoot}, nil
}

func (s *LocalStore) Close() error {
	return nil
}

func (s *LocalStore) Driver() string {
	return "local"
}

func (s *LocalStore) Root() string {
	return s.root
}

func (s *LocalStore) NewReader(_ context.Context, physicalHash string) (io.ReadCloser, error) {
	return os.Open(s.objectPath(physicalHash))
}

func (s *LocalStore) NewRangeReader(_ context.Context, physicalHash string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("range offset must be non-negative")
	}
	file, err := os.Open(s.objectPath(physicalHash))
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}

	var reader io.Reader = file
	if length >= 0 {
		reader = io.LimitReader(file, length)
	}
	return &localRangeReader{reader: reader, file: file}, nil
}

func (s *LocalStore) NewWriter(_ context.Context, physicalHash string) (io.WriteCloser, error) {
	finalPath := s.objectPath(physicalHash)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return nil, err
	}
	tempFile, err := os.CreateTemp(filepath.Dir(finalPath), "."+filepath.Base(finalPath)+".tmp-*")
	if err != nil {
		return nil, err
	}
	return &localWriter{
		file:      tempFile,
		tempPath:  tempFile.Name(),
		finalPath: finalPath,
	}, nil
}

func (s *LocalStore) WriteUploadChunk(ctx context.Context, uploadID string, offset int64, source io.Reader) (int64, error) {
	if offset < 0 {
		return 0, fmt.Errorf("upload offset must be non-negative")
	}
	stagingPath, err := s.uploadPath(uploadID)
	if err != nil {
		return 0, err
	}
	s.uploadMutex.Lock()
	defer s.uploadMutex.Unlock()
	if err := os.MkdirAll(filepath.Dir(stagingPath), 0o755); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(stagingPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() != offset {
		return info.Size(), fmt.Errorf("%w: got %d, want %d", ErrUploadOffsetConflict, offset, info.Size())
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	written, writeErr := io.Copy(file, &contextReader{ctx: ctx, reader: source})
	committed := offset + written
	if syncErr := file.Sync(); writeErr == nil && syncErr != nil {
		writeErr = syncErr
	}
	return committed, writeErr
}

func (s *LocalStore) UploadOffset(_ context.Context, uploadID string) (int64, bool, error) {
	stagingPath, err := s.uploadPath(uploadID)
	if err != nil {
		return 0, false, err
	}
	s.uploadMutex.Lock()
	defer s.uploadMutex.Unlock()
	info, err := os.Stat(stagingPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return info.Size(), true, nil
}

func (s *LocalStore) TruncateUpload(_ context.Context, uploadID string, offset int64) error {
	if offset < 0 {
		return fmt.Errorf("upload offset must be non-negative")
	}
	stagingPath, err := s.uploadPath(uploadID)
	if err != nil {
		return err
	}
	s.uploadMutex.Lock()
	defer s.uploadMutex.Unlock()
	file, err := os.OpenFile(stagingPath, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if offset > info.Size() {
		return fmt.Errorf("rollback offset %d exceeds committed size %d", offset, info.Size())
	}
	if err := file.Truncate(offset); err != nil {
		return err
	}
	return file.Sync()
}

func (s *LocalStore) CompleteUpload(_ context.Context, uploadID, objectName string, size int64, existingPhysicalHash *string) error {
	stagingPath, err := s.uploadPath(uploadID)
	if err != nil {
		return err
	}
	finalPath := s.objectPath(objectName)
	s.uploadMutex.Lock()
	defer s.uploadMutex.Unlock()
	info, err := os.Stat(stagingPath)
	if errors.Is(err, os.ErrNotExist) {
		// Retrying complete after an atomic rename is safe because the session
		// has already persisted status=uploaded and the final size is verified.
		finalInfo, finalErr := os.Stat(finalPath)
		if finalErr == nil && finalInfo.Size() == size {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}
	if info.Size() != size {
		return fmt.Errorf("uploaded size %d does not match declared size %d", info.Size(), size)
	}
	if existingPhysicalHash == nil || *existingPhysicalHash != objectName {
		if _, err := os.Stat(finalPath); err == nil {
			return fmt.Errorf("%w: %s", ErrObjectCollision, objectName)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return err
	}
	return os.Rename(stagingPath, finalPath)
}

func (s *LocalStore) CancelUpload(_ context.Context, uploadID string) error {
	stagingPath, err := s.uploadPath(uploadID)
	if err != nil {
		return err
	}
	s.uploadMutex.Lock()
	defer s.uploadMutex.Unlock()
	err = os.Remove(stagingPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *LocalStore) Delete(_ context.Context, physicalHash string) error {
	if strings.TrimSpace(physicalHash) == "" {
		return nil
	}
	err := os.Remove(s.objectPath(physicalHash))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *LocalStore) List(ctx context.Context) ([]ObjectInfo, error) {
	var objects []ObjectInfo
	err := filepath.WalkDir(s.root, func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if filePath != s.root && isReservedObject(filepath.ToSlash(strings.TrimPrefix(filePath, s.root))) {
				return fs.SkipDir
			}
			return nil
		}
		name, err := filepath.Rel(s.root, filePath)
		if err != nil {
			return err
		}
		if isLocalTempObject(filepath.Base(name)) {
			return nil
		}
		if isReservedObject(filepath.ToSlash(name)) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		objects = append(objects, ObjectInfo{
			Name: filepath.ToSlash(name),
			Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return objects, nil
}

func isLocalTempObject(base string) bool {
	return strings.HasPrefix(base, ".") && strings.Contains(base, ".tmp-")
}

func (s *LocalStore) objectPath(physicalHash string) string {
	cleaned := filepath.Clean("/" + strings.TrimPrefix(physicalHash, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	return filepath.Join(s.root, cleaned)
}

func (s *LocalStore) uploadPath(uploadID string) (string, error) {
	uploadID = strings.TrimSpace(uploadID)
	if uploadID == "" || filepath.Base(uploadID) != uploadID || uploadID == "." {
		return "", errors.New("invalid upload ID")
	}
	return filepath.Join(s.root, "_vfs-link", "uploads", uploadID+".part"), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

func (w *localWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, os.ErrClosed
	}
	return w.file.Write(p)
}

func (w *localWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.file.Close(); err != nil {
		_ = os.Remove(w.tempPath)
		return err
	}
	if err := os.Rename(w.tempPath, w.finalPath); err != nil {
		_ = os.Remove(w.tempPath)
		return err
	}
	return nil
}

func (w *localWriter) CloseWithError(_ error) error {
	if w.closed {
		return nil
	}
	w.closed = true
	closeErr := w.file.Close()
	removeErr := os.Remove(w.tempPath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}

func (r *localRangeReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *localRangeReader) Close() error {
	return r.file.Close()
}

var _ LocalResumableUploadStore = (*LocalStore)(nil)
