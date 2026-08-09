package webdav

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
	xwebdav "golang.org/x/net/webdav"
)

type FileSystem struct {
	store    metadataStore
	objects  blob.Store
	commands commandService
}

// commandService is owned by the WebDAV adapter. It keeps protocol handling
// dependent on application commands instead of persistence mutation methods.
type commandService interface {
	CreateDirectory(context.Context, string) (db.FileRecord, error)
	Relocate(context.Context, string, string) (fileops.MutationOutcome, error)
	DeleteToTrash(context.Context, []string) (fileops.MutationOutcome, error)
	PublishUploaded(context.Context, fileops.PublishIntent) (fileops.PublishResult, error)
	WaitVisible(context.Context, string) (db.OperationRecord, error)
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
	return NewFileSystemWithCommands(store, objects, &legacyCommands{store: store})
}

// NewFileSystemWithCommands injects the common application mutation boundary.
// New code, including production wiring, should use this constructor. The
// compatibility constructor above remains for downstream read-oriented users.
func NewFileSystemWithCommands(store metadataStore, objects blob.Store, commands commandService) *FileSystem {
	if commands == nil {
		panic("WebDAV command service is required")
	}
	return &FileSystem{store: store, objects: objects, commands: commands}
}

func (fs *FileSystem) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	name = cleanDAVPath(name)
	if name == "/" {
		return os.ErrExist
	}
	domainPath := logicpath.FromProtocol(name)
	if _, found, err := fs.store.Find(ctx, domainPath); err != nil {
		return err
	} else if found {
		return os.ErrExist
	}
	_, err := fs.commands.CreateDirectory(ctx, domainPath)
	return commandError(err)
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
		return newUploadFile(ctx, fs.store, fs.commands, fs.objects, domainPath)
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
	outcome, err := fs.commands.DeleteToTrash(ctx, []string{domainPath})
	if err != nil {
		return commandError(err)
	}
	return fs.waitVisible(ctx, outcome)
}

func (fs *FileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldName, newName = cleanDAVPath(oldName), cleanDAVPath(newName)
	if oldName == "/" || newName == "/" {
		return os.ErrPermission
	}
	if newName == oldName || strings.HasPrefix(newName, withTrailingSlash(oldName)) {
		return os.ErrPermission
	}
	outcome, err := fs.commands.Relocate(ctx, logicpath.FromProtocol(oldName), logicpath.FromProtocol(newName))
	if err != nil {
		return commandError(err)
	}
	return fs.waitVisible(ctx, outcome)
}

func (fs *FileSystem) waitVisible(ctx context.Context, outcome fileops.MutationOutcome) error {
	if outcome.Operation == nil {
		return nil
	}
	_, err := fs.commands.WaitVisible(ctx, outcome.Operation.ID)
	return commandError(err)
}

func commandError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, db.ErrNotFound):
		return os.ErrNotExist
	case errors.Is(err, db.ErrPathConflict), errors.Is(err, db.ErrIsDirectory):
		return os.ErrExist
	case errors.Is(err, db.ErrInvalidMove):
		return os.ErrPermission
	default:
		return err
	}
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
