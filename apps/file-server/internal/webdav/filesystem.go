package webdav

import (
	"context"
	"os"
	"path"
	"strings"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
	xwebdav "golang.org/x/net/webdav"
)

type FileSystem struct {
	store   metadataStore
	objects blob.Store
}

type metadataStore interface {
	Find(context.Context, string) (db.FileRecord, bool, error)
	ListDirectChildren(context.Context, string, db.DirectChildrenOptions) (db.DirectChildrenPage, error)
	ListPrefix(context.Context, string) ([]db.FileRecord, error)
	UpsertDirectory(context.Context, string) error
	ReplaceFile(context.Context, string, string, int64) (string, error)
	ReplaceFileConditional(context.Context, string, string, int64, *string, bool) (string, bool, error)
	RenamePath(context.Context, string, string) error
	DeletePath(context.Context, string) error
	DeletePrefix(context.Context, string) error
}

func NewFileSystem(store metadataStore, objects blob.Store) *FileSystem {
	return &FileSystem{store: store, objects: objects}
}

func (fs *FileSystem) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	name = cleanDAVPath(name)
	if name == "/" {
		return os.ErrExist
	}
	if err := fs.requireDirectory(ctx, path.Dir(name)); err != nil {
		return err
	}
	domainPath := logicpath.FromProtocol(name)
	if _, found, err := fs.store.Find(ctx, domainPath); err != nil {
		return err
	} else if found {
		return os.ErrExist
	}
	return fs.store.UpsertDirectory(ctx, domainPath)
}

func (fs *FileSystem) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (xwebdav.File, error) {
	name = cleanDAVPath(name)
	if flag&(os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		if name == "/" || flag&os.O_APPEND != 0 {
			return nil, os.ErrPermission
		}
		if err := fs.requireDirectory(ctx, path.Dir(name)); err != nil {
			return nil, err
		}
		domainPath := logicpath.FromProtocol(name)
		if record, found, err := fs.store.Find(ctx, domainPath); err != nil {
			return nil, err
		} else if found && record.IsDirectory {
			return nil, os.ErrPermission
		}
		return newUploadFile(ctx, fs.store, fs.objects, domainPath)
	}
	if name == "/" {
		entries, err := fs.listDirectory(ctx, name)
		if err != nil {
			return nil, err
		}
		return &directoryFile{name: name, info: rootInfo(), entries: entries}, nil
	}
	record, found, err := fs.store.Find(ctx, logicpath.FromProtocol(name))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	info := infoFromRecord(record)
	if record.IsDirectory {
		entries, err := fs.listDirectory(ctx, name)
		if err != nil {
			return nil, err
		}
		return &directoryFile{name: name, info: info, entries: entries}, nil
	}
	return &readFile{ctx: ctx, objects: fs.objects, record: record, info: info}, nil
}

func (fs *FileSystem) RemoveAll(ctx context.Context, name string) error {
	name = cleanDAVPath(name)
	if name == "/" {
		return os.ErrPermission
	}
	domainPath := logicpath.FromProtocol(name)
	record, found, err := fs.store.Find(ctx, domainPath)
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	if !record.IsDirectory {
		if err := fs.store.DeletePath(ctx, domainPath); err != nil {
			return err
		}
		_ = fs.objects.Delete(ctx, record.PhysicalHash)
		return nil
	}
	prefix := logicpath.WithTrailingSlash(domainPath)
	children, err := fs.store.ListPrefix(ctx, prefix)
	if err != nil {
		return err
	}
	if err := fs.store.DeletePrefix(ctx, prefix); err != nil {
		return err
	}
	if err := fs.store.DeletePath(ctx, domainPath); err != nil {
		return err
	}
	for _, child := range children {
		if !child.IsDirectory {
			_ = fs.objects.Delete(ctx, child.PhysicalHash)
		}
	}
	return nil
}

func (fs *FileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldName, newName = cleanDAVPath(oldName), cleanDAVPath(newName)
	if oldName == "/" || newName == "/" {
		return os.ErrPermission
	}
	if newName == oldName || strings.HasPrefix(newName, withTrailingSlash(oldName)) {
		return os.ErrPermission
	}
	if err := fs.requireDirectory(ctx, path.Dir(newName)); err != nil {
		return err
	}
	return fs.store.RenamePath(ctx, logicpath.FromProtocol(oldName), logicpath.FromProtocol(newName))
}

func (fs *FileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	name = cleanDAVPath(name)
	if name == "/" {
		return rootInfo(), nil
	}
	record, found, err := fs.store.Find(ctx, logicpath.FromProtocol(name))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	return infoFromRecord(record), nil
}

func (fs *FileSystem) requireDirectory(ctx context.Context, name string) error {
	if name == "/" {
		return nil
	}
	record, found, err := fs.store.Find(ctx, logicpath.FromProtocol(name))
	if err != nil {
		return err
	}
	if !found || !record.IsDirectory {
		return os.ErrNotExist
	}
	return nil
}

func (fs *FileSystem) listDirectory(ctx context.Context, name string) ([]os.FileInfo, error) {
	page, err := fs.store.ListDirectChildren(ctx, logicpath.FromProtocol(name), db.DirectChildrenOptions{})
	if err != nil {
		return nil, err
	}
	entries := make([]os.FileInfo, 0, len(page.Records))
	for _, record := range page.Records {
		entries = append(entries, infoFromRecord(record))
	}
	return entries, nil
}

func cleanDAVPath(name string) string {
	if name == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(name, "/"))
}

func withTrailingSlash(name string) string {
	if name == "/" || strings.HasSuffix(name, "/") {
		return name
	}
	return name + "/"
}

var _ xwebdav.FileSystem = (*FileSystem)(nil)
