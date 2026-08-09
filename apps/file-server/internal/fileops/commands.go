package fileops

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
)

const operationPollInterval = 25 * time.Millisecond

// MutationOutcome is the protocol-neutral result of a namespace mutation.
// Immediate mutations return Records. Durable mutations return Operation and
// can be joined with WaitVisible by synchronous protocol adapters.
type MutationOutcome struct {
	Records   []db.FileRecord
	Operation *db.OperationRecord
}

// PublishIntent describes an object that has already been durably written and
// is ready to become visible in the logical namespace.
type PublishIntent struct {
	LogicPath            string
	PhysicalHash         string
	Size                 int64
	ExpectedPhysicalHash *string
	RequireAbsent        bool
}

// PublishResult separates the visible namespace commit from best-effort
// cleanup. Once Published is set, callers must report the write as successful;
// cleanup failures are repaired by retry/drift instead of rolling back the
// logical mapping.
type PublishResult struct {
	Published      db.FileRecord
	PreviousObject string
	CleanupPending bool
	CleanupErrors  []error
}

// CreateDirectory creates a directory through the common application
// boundary. Existing directories are idempotent; existing files conflict.
func (s *Service) CreateDirectory(ctx context.Context, rawPath string) (db.FileRecord, error) {
	logicPath, err := logicpath.Parse(rawPath)
	if err != nil {
		return db.FileRecord{}, err
	}
	if logicPath == "" {
		return db.FileRecord{LogicPath: "", IsDirectory: true}, nil
	}
	if existing, found, err := s.store.Find(ctx, logicPath); err != nil {
		return db.FileRecord{}, err
	} else if found {
		if !existing.IsDirectory {
			return db.FileRecord{}, fmt.Errorf("%w: %s", db.ErrPathConflict, logicPath)
		}
		return existing, nil
	}
	if err := s.requireDirectory(ctx, logicpath.Parent(logicPath)); err != nil {
		return db.FileRecord{}, err
	}
	if err := s.store.UpsertDirectory(ctx, logicPath); err != nil {
		return db.FileRecord{}, err
	}
	record, found, err := s.store.Find(ctx, logicPath)
	if err != nil {
		return db.FileRecord{}, err
	}
	if !found || !record.IsDirectory {
		return db.FileRecord{}, fmt.Errorf("%w: %s", db.ErrNotFound, logicPath)
	}
	return record, nil
}

// Relocate moves or renames an item to an exact target path. It is the common
// MOVE/RNTO application boundary; protocol adapters must not call RenamePath
// directly.
func (s *Service) Relocate(ctx context.Context, rawSource, rawTarget string) (MutationOutcome, error) {
	if err := s.waitForResume(ctx); err != nil {
		return MutationOutcome{}, err
	}
	source, err := logicpath.Parse(rawSource)
	if err != nil {
		return MutationOutcome{}, err
	}
	target, err := logicpath.Parse(rawTarget)
	if err != nil {
		return MutationOutcome{}, err
	}
	if source == "" || target == "" || source == target || logicpath.IsDescendant(source, target) {
		return MutationOutcome{}, db.ErrInvalidMove
	}
	record, found, err := s.store.Find(ctx, source)
	if err != nil {
		return MutationOutcome{}, err
	}
	if !found {
		return MutationOutcome{}, fmt.Errorf("%w: %s", db.ErrNotFound, source)
	}
	if _, exists, err := s.store.Find(ctx, target); err != nil {
		return MutationOutcome{}, err
	} else if exists {
		return MutationOutcome{}, fmt.Errorf("%w: %s", db.ErrPathConflict, target)
	}
	if err := s.requireDirectory(ctx, logicpath.Parent(target)); err != nil {
		return MutationOutcome{}, err
	}

	// Same-parent rename retains the v4 single-shard fast path and the v3
	// durable directory operation. Cross-parent exact moves use the store's
	// atomic namespace primitive because the existing BatchMove contract takes
	// a destination directory rather than an exact target.
	if logicpath.Parent(source) == logicpath.Parent(target) {
		result, err := s.Rename(ctx, source, path.Base(target))
		return MutationOutcome{Records: result.Records, Operation: result.Operation}, err
	}
	if err := s.store.RenamePath(ctx, source, target); err != nil {
		return MutationOutcome{}, err
	}
	moved, found, err := s.store.Find(ctx, target)
	if err != nil {
		return MutationOutcome{}, err
	}
	if !found {
		return MutationOutcome{}, fmt.Errorf("%w: %s", db.ErrNotFound, target)
	}
	_ = record // record lookup validates the source before the mutation.
	return MutationOutcome{Records: []db.FileRecord{moved}}, nil
}

// DeleteToTrash is the common DELETE/DELE/RMD boundary. Archive objects and
// thumbnails remain available for restore until an explicit permanent-delete
// operation completes.
func (s *Service) DeleteToTrash(ctx context.Context, paths []string) (MutationOutcome, error) {
	result, err := s.Trash(ctx, paths)
	return MutationOutcome{Records: result.Records, Operation: result.Operation}, err
}

// WaitVisible joins a durable namespace operation. Current operation runners
// mark Completed only after the namespace mutation is visible, so cleanup that
// is not part of that namespace commit must remain best-effort/deferred.
func (s *Service) WaitVisible(ctx context.Context, operationID string) (db.OperationRecord, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return db.OperationRecord{}, errors.New("operation id is required")
	}
	ticker := time.NewTicker(operationPollInterval)
	defer ticker.Stop()
	for {
		operation, found, err := s.Operation(ctx, operationID)
		if err != nil {
			return db.OperationRecord{}, err
		}
		if !found {
			return db.OperationRecord{}, fmt.Errorf("%w: operation %s", db.ErrNotFound, operationID)
		}
		switch operation.Status {
		case db.OperationStatusCompleted:
			return operation, nil
		case db.OperationStatusFailed:
			if strings.TrimSpace(operation.Error) == "" {
				return operation, errors.New("file operation failed")
			}
			return operation, errors.New(operation.Error)
		}
		select {
		case <-ctx.Done():
			return db.OperationRecord{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// PublishUploaded atomically publishes an already-written object through the
// metadata CAS contract and then performs best-effort cleanup. Retrying after a
// lost response is idempotent when the current mapping already equals the
// intended object and size.
func (s *Service) PublishUploaded(ctx context.Context, intent PublishIntent) (PublishResult, error) {
	logicPath, err := logicpath.Parse(intent.LogicPath)
	if err != nil {
		return PublishResult{}, err
	}
	if logicPath == "" || strings.TrimSpace(intent.PhysicalHash) == "" || intent.Size < 0 {
		return PublishResult{}, errors.New("valid file path, physical object, and size are required")
	}
	if err := s.ensureDirectories(ctx, logicpath.Parent(logicPath)); err != nil {
		return PublishResult{}, err
	}
	previous, matched, err := s.store.ReplaceFileConditional(
		ctx, logicPath, intent.PhysicalHash, intent.Size,
		intent.ExpectedPhysicalHash, intent.RequireAbsent,
	)
	if err != nil {
		return PublishResult{}, err
	}
	if !matched {
		current, found, findErr := s.store.Find(ctx, logicPath)
		if findErr != nil {
			return PublishResult{}, findErr
		}
		if !found || current.IsDirectory || current.PhysicalHash != intent.PhysicalHash || current.Size != intent.Size {
			return PublishResult{}, db.ErrPathConflict
		}
		return PublishResult{Published: current}, nil
	}
	published, found, err := s.store.Find(ctx, logicPath)
	if err != nil {
		return PublishResult{}, err
	}
	if !found || published.IsDirectory {
		return PublishResult{}, fmt.Errorf("%w: %s", db.ErrNotFound, logicPath)
	}
	result := PublishResult{Published: published, PreviousObject: previous}
	thumbnails, detachErr := s.store.DetachThumbnails(ctx, []int{published.ID})
	if detachErr != nil {
		result.CleanupPending = true
		result.CleanupErrors = append(result.CleanupErrors, fmt.Errorf("detach stale thumbnail: %w", detachErr))
	} else {
		for _, thumbnail := range thumbnails {
			if err := s.thumbnailObjects.Delete(ctx, thumbnail.PhysicalHash); err != nil {
				result.CleanupPending = true
				result.CleanupErrors = append(result.CleanupErrors, fmt.Errorf("delete stale thumbnail %s: %w", thumbnail.ID, err))
			}
		}
	}
	if previous != "" && previous != intent.PhysicalHash {
		if err := s.objects.Delete(ctx, previous); err != nil {
			result.CleanupPending = true
			result.CleanupErrors = append(result.CleanupErrors, fmt.Errorf("delete previous object %s: %w", previous, err))
		}
	}
	return result, nil
}

func (s *Service) requireDirectory(ctx context.Context, logicPath string) error {
	if logicPath == "" {
		return nil
	}
	record, found, err := s.store.Find(ctx, logicPath)
	if err != nil {
		return err
	}
	if !found || !record.IsDirectory {
		return fmt.Errorf("%w: parent directory %s", db.ErrNotFound, logicPath)
	}
	return nil
}

func (s *Service) ensureDirectories(ctx context.Context, parent string) error {
	if parent == "" {
		return nil
	}
	parents := make([]string, 0, strings.Count(parent, "/")+1)
	for current := parent; current != ""; current = logicpath.Parent(current) {
		parents = append(parents, current)
	}
	for index := len(parents) - 1; index >= 0; index-- {
		if _, err := s.CreateDirectory(ctx, parents[index]); err != nil {
			return err
		}
	}
	return nil
}
