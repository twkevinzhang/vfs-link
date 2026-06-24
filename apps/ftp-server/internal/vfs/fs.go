package vfs

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/gcs"
)

type FS struct {
	store   *db.Store
	objects *gcs.Client
}

func New(store *db.Store, objects *gcs.Client) *FS {
	return &FS{
		store:   store,
		objects: objects,
	}
}

func (fs *FS) Name() string {
	return "vfs-link-db-gcs"
}

func (fs *FS) Create(name string) (afero.File, error) {
	return newUploadFile(fs.store, fs.objects, cleanPath(name)), nil
}

func (fs *FS) Mkdir(name string, _ os.FileMode) error {
	logicPath := cleanPath(name)
	_, found, err := fs.store.Find(context.Background(), logicPath)
	if err != nil {
		return err
	}
	if found {
		return os.ErrExist
	}
	return fs.store.UpsertDirectory(context.Background(), logicPath)
}

func (fs *FS) MkdirAll(name string, perm os.FileMode) error {
	cleaned := cleanPath(name)
	if cleaned == "/" {
		return nil
	}

	current := ""
	for _, part := range strings.Split(strings.Trim(cleaned, "/"), "/") {
		current += "/" + part
		if err := fs.Mkdir(current, perm); err != nil {
			return err
		}
	}
	return nil
}

func (fs *FS) Open(name string) (afero.File, error) {
	logicPath := cleanPath(name)
	info, record, err := fs.statRecord(context.Background(), logicPath)
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		entries, err := fs.listDirectChildren(context.Background(), logicPath)
		if err != nil {
			return nil, err
		}
		return newDirFile(logicPath, info, entries), nil
	}

	reader, err := fs.objects.NewReader(context.Background(), record.PhysicalHash)
	if err != nil {
		return nil, err
	}
	return newReadFile(logicPath, info, reader), nil
}

func (fs *FS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		if flag&os.O_APPEND != 0 {
			return nil, fmt.Errorf("append mode is not supported: %s", name)
		}
		return fs.Create(name)
	}
	return fs.Open(name)
}

func (fs *FS) Remove(name string) error {
	return fs.removePath(context.Background(), cleanPath(name))
}

func (fs *FS) RemoveAll(name string) error {
	return fs.Remove(name)
}

func (fs *FS) Rename(oldname, newname string) error {
	return fs.store.RenamePath(context.Background(), cleanPath(oldname), cleanPath(newname))
}

func (fs *FS) Stat(name string) (os.FileInfo, error) {
	info, _, err := fs.statRecord(context.Background(), cleanPath(name))
	return info, err
}

func (fs *FS) Chmod(string, os.FileMode) error {
	return nil
}

func (fs *FS) Chown(string, int, int) error {
	return nil
}

func (fs *FS) Chtimes(name string, _, _ time.Time) error {
	_, found, err := fs.store.Find(context.Background(), cleanPath(name))
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	return nil
}

func (fs *FS) statRecord(ctx context.Context, logicPath string) (os.FileInfo, db.FileRecord, error) {
	if logicPath == "/" {
		return rootInfo(), db.FileRecord{LogicPath: "/", IsDirectory: true, UpdatedAt: time.Now()}, nil
	}

	record, found, err := fs.store.Find(ctx, logicPath)
	if err != nil {
		return nil, db.FileRecord{}, err
	}
	if !found {
		return nil, db.FileRecord{}, os.ErrNotExist
	}
	return infoFromRecord(record), record, nil
}

func (fs *FS) listDirectChildren(ctx context.Context, dirPath string) ([]os.FileInfo, error) {
	prefix := withTrailingSlash(dirPath)
	records, err := fs.store.ListPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}

	entries := make([]os.FileInfo, 0, len(records))
	for _, record := range records {
		suffix := strings.TrimPrefix(record.LogicPath, prefix)
		if suffix == "" || strings.Contains(suffix, "/") {
			continue
		}
		entries = append(entries, infoFromRecord(record))
	}
	return entries, nil
}

func (fs *FS) removePath(ctx context.Context, logicPath string) error {
	record, found, err := fs.store.Find(ctx, logicPath)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	if record.IsDirectory {
		prefix := withTrailingSlash(logicPath)
		children, err := fs.store.ListPrefix(ctx, prefix)
		if err != nil {
			return err
		}
		for _, child := range children {
			if !child.IsDirectory {
				if err := fs.objects.Delete(ctx, child.PhysicalHash); err != nil {
					return err
				}
			}
		}
		if err := fs.store.DeletePrefix(ctx, prefix); err != nil {
			return err
		}
	} else if err := fs.objects.Delete(ctx, record.PhysicalHash); err != nil {
		return err
	}

	return fs.store.DeletePath(ctx, logicPath)
}

func cleanPath(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return "/"
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	cleaned := path.Clean(name)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func withTrailingSlash(value string) string {
	if value == "/" {
		return "/"
	}
	if strings.HasSuffix(value, "/") {
		return value
	}
	return value + "/"
}

func pathBase(value string) string {
	if value == "/" {
		return "/"
	}
	return path.Base(value)
}

func now() time.Time {
	return time.Now()
}

var _ afero.Fs = (*FS)(nil)
