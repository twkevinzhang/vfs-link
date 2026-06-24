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
)

type LocalStore struct {
	root string
}

type localWriter struct {
	file      *os.File
	tempPath  string
	finalPath string
	closed    bool
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
			return nil
		}
		name, err := filepath.Rel(s.root, filePath)
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.Base(name), ".") {
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

func (s *LocalStore) objectPath(physicalHash string) string {
	cleaned := filepath.Clean("/" + strings.TrimPrefix(physicalHash, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	return filepath.Join(s.root, cleaned)
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
