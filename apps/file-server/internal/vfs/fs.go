package vfs

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
)

const defaultCommandTimeout = 30 * time.Second

// FileCommands is the mutation boundary used by the FTP/afero adapter. Read
// operations remain on the query store, while every namespace or publication
// mutation is delegated to the shared application service.
type FileCommands interface {
	CreateDirectory(context.Context, string) (db.FileRecord, error)
	Relocate(context.Context, string, string) (fileops.MutationOutcome, error)
	DeleteToTrash(context.Context, []string) (fileops.MutationOutcome, error)
	WaitVisible(context.Context, string) (db.OperationRecord, error)
	PublishUploaded(context.Context, fileops.PublishIntent) (fileops.PublishResult, error)
}

type FS struct {
	store          db.Store
	objects        blob.Store
	commands       FileCommands
	commandTimeout time.Duration
}

func New(store db.Store, objects blob.Store) *FS {
	var commands FileCommands
	if store != nil && objects != nil {
		commands = fileops.New(store, objects, objects)
	}
	return NewWithCommands(store, objects, commands, defaultCommandTimeout)
}

func NewWithCommands(store db.Store, objects blob.Store, commands FileCommands, commandTimeout time.Duration) *FS {
	if commandTimeout <= 0 {
		commandTimeout = defaultCommandTimeout
	}
	return &FS{store: store, objects: objects, commands: commands, commandTimeout: commandTimeout}
}

func (fs *FS) Name() string {
	return "vfs-link"
}

func (fs *FS) Create(name string) (afero.File, error) {
	return newUploadFile(fs.store, fs.objects, fs.commands, fs.commandTimeout, cleanPath(name)), nil
}

func (fs *FS) Mkdir(name string, _ os.FileMode) error {
	logicPath := cleanPath(name)
	if fs.commands != nil {
		ctx, cancel := fs.commandContext()
		defer cancel()
		_, err := fs.commands.CreateDirectory(ctx, logicPath)
		return err
	}
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
	if cleaned == "" {
		return nil
	}

	current := ""
	for _, part := range strings.Split(cleaned, "/") {
		current = logicpath.Join(current, part)
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
	logicPath := cleanPath(name)
	if fs.commands == nil {
		return fs.removePath(context.Background(), logicPath)
	}
	ctx, cancel := fs.commandContext()
	defer cancel()
	result, err := fs.commands.DeleteToTrash(ctx, []string{logicPath})
	if err != nil {
		return err
	}
	if result.Operation != nil {
		_, err = fs.commands.WaitVisible(ctx, result.Operation.ID)
	}
	return err
}

func (fs *FS) RemoveAll(name string) error {
	return fs.Remove(name)
}

func (fs *FS) Rename(oldname, newname string) error {
	if fs.commands == nil {
		return fs.store.RenamePath(context.Background(), cleanPath(oldname), cleanPath(newname))
	}
	ctx, cancel := fs.commandContext()
	defer cancel()
	result, err := fs.commands.Relocate(ctx, cleanPath(oldname), cleanPath(newname))
	if err != nil {
		return err
	}
	if result.Operation != nil {
		_, err = fs.commands.WaitVisible(ctx, result.Operation.ID)
	}
	return err
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

func (fs *FS) commandContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), fs.commandTimeout)
}

func (fs *FS) statRecord(ctx context.Context, logicPath string) (os.FileInfo, db.FileRecord, error) {
	if logicPath == "" {
		return rootInfo(), db.FileRecord{LogicPath: "", IsDirectory: true, UpdatedAt: time.Now()}, nil
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
	return logicpath.FromProtocol(name)
}

func withTrailingSlash(value string) string {
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, "/") {
		return value
	}
	return value + "/"
}

func pathBase(value string) string {
	if value == "" {
		return "/"
	}
	return path.Base(value)
}

func now() time.Time {
	return time.Now()
}

var _ afero.Fs = (*FS)(nil)
