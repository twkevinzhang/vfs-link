package webdav

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
)

// legacyCommands preserves NewFileSystem compatibility for embedders that do
// not yet inject the application service. Production must use
// NewFileSystemWithCommands so all mutations receive durable-operation and
// cleanup semantics from fileops.Service.
type legacyCommands struct {
	store metadataStore
}

func (c *legacyCommands) CreateDirectory(ctx context.Context, logicPath string) (db.FileRecord, error) {
	if err := c.store.UpsertDirectory(ctx, logicPath); err != nil {
		return db.FileRecord{}, err
	}
	record, found, err := c.store.Find(ctx, logicPath)
	if err != nil {
		return db.FileRecord{}, err
	}
	if !found {
		return db.FileRecord{}, db.ErrNotFound
	}
	return record, nil
}

func (c *legacyCommands) Relocate(ctx context.Context, source, target string) (fileops.MutationOutcome, error) {
	if err := c.store.RenamePath(ctx, source, target); err != nil {
		return fileops.MutationOutcome{}, err
	}
	record, found, err := c.store.Find(ctx, target)
	if err != nil {
		return fileops.MutationOutcome{}, err
	}
	if !found {
		return fileops.MutationOutcome{}, db.ErrNotFound
	}
	return fileops.MutationOutcome{Records: []db.FileRecord{record}}, nil
}

func (c *legacyCommands) DeleteToTrash(ctx context.Context, paths []string) (fileops.MutationOutcome, error) {
	if trash, ok := c.store.(interface {
		TrashPaths(context.Context, []db.TrashPath) ([]db.FileRecord, error)
	}); ok {
		items := make([]db.TrashPath, 0, len(paths))
		for _, logicPath := range paths {
			items = append(items, db.TrashPath{Path: logicPath, TrashID: uuid.NewString()})
		}
		records, err := trash.TrashPaths(ctx, items)
		return fileops.MutationOutcome{Records: records}, err
	}
	for _, logicPath := range paths {
		record, found, err := c.store.Find(ctx, logicPath)
		if err != nil {
			return fileops.MutationOutcome{}, err
		}
		if !found {
			return fileops.MutationOutcome{}, db.ErrNotFound
		}
		if record.IsDirectory {
			if err := c.store.DeletePrefix(ctx, logicpath.WithTrailingSlash(logicPath)); err != nil {
				return fileops.MutationOutcome{}, err
			}
		}
		if err := c.store.DeletePath(ctx, logicPath); err != nil {
			return fileops.MutationOutcome{}, err
		}
	}
	return fileops.MutationOutcome{}, nil
}

func (c *legacyCommands) PublishUploaded(ctx context.Context, intent fileops.PublishIntent) (fileops.PublishResult, error) {
	previous, matched, err := c.store.ReplaceFileConditional(
		ctx, intent.LogicPath, intent.PhysicalHash, intent.Size,
		intent.ExpectedPhysicalHash, intent.RequireAbsent,
	)
	if err != nil {
		return fileops.PublishResult{}, err
	}
	if !matched {
		current, found, findErr := c.store.Find(ctx, intent.LogicPath)
		if findErr != nil {
			return fileops.PublishResult{}, findErr
		}
		if !found || current.IsDirectory || current.PhysicalHash != intent.PhysicalHash || current.Size != intent.Size {
			return fileops.PublishResult{}, db.ErrPathConflict
		}
		return fileops.PublishResult{Published: current}, nil
	}
	published, found, err := c.store.Find(ctx, intent.LogicPath)
	if err != nil {
		return fileops.PublishResult{}, err
	}
	if !found {
		return fileops.PublishResult{}, fmt.Errorf("%w: %s", db.ErrNotFound, intent.LogicPath)
	}
	return fileops.PublishResult{Published: published, PreviousObject: previous}, nil
}

func (*legacyCommands) WaitVisible(context.Context, string) (db.OperationRecord, error) {
	return db.OperationRecord{}, errUnsupported
}

var _ commandService = (*legacyCommands)(nil)
