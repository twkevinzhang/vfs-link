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
	ExpectedSnapshot     *db.FileSnapshot
	RequireAbsent        bool
	// DeferCleanup returns the previous-object evidence without deleting it.
	// Durable upload completion uses this mode so it can persist that evidence
	// before any irreversible cleanup side effect.
	DeferCleanup bool
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

type UploadedCleanupIntent struct {
	LogicPath             string
	PublishedPhysicalHash string
	PreviousPhysicalHash  string
}

type UploadedCleanupResult struct {
	Pending bool
	Errors  []error
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
	var previous string
	var matched bool
	if intent.ExpectedSnapshot != nil {
		previous, matched, err = s.store.ReplaceFileConditionalSnapshot(
			ctx, logicPath, intent.PhysicalHash, intent.Size,
			intent.ExpectedSnapshot, intent.RequireAbsent,
		)
	} else {
		previous, matched, err = s.store.ReplaceFileConditional(
			ctx, logicPath, intent.PhysicalHash, intent.Size,
			intent.ExpectedPhysicalHash, intent.RequireAbsent,
		)
	}
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
		previous = expectedPreviousObject(intent)
		result := PublishResult{Published: current, PreviousObject: previous}
		if intent.DeferCleanup {
			result.CleanupPending = true
		}
		return result, nil
	}
	published, found, err := s.store.Find(ctx, logicPath)
	if err != nil {
		return PublishResult{}, err
	}
	if !found || published.IsDirectory {
		return PublishResult{}, fmt.Errorf("%w: %s", db.ErrNotFound, logicPath)
	}
	result := PublishResult{Published: published, PreviousObject: previous}
	if intent.DeferCleanup {
		result.CleanupPending = true
		return result, nil
	}
	cleanup := s.RetryUploadedCleanup(ctx, UploadedCleanupIntent{
		LogicPath: logicPath, PublishedPhysicalHash: intent.PhysicalHash,
		PreviousPhysicalHash: previous,
	})
	result.CleanupPending = cleanup.Pending
	result.CleanupErrors = append(result.CleanupErrors, cleanup.Errors...)
	return result, nil
}

func expectedPreviousObject(intent PublishIntent) string {
	if intent.ExpectedSnapshot != nil && strings.TrimSpace(intent.ExpectedSnapshot.PhysicalHash) != "" {
		return intent.ExpectedSnapshot.PhysicalHash
	}
	if intent.ExpectedPhysicalHash != nil {
		return *intent.ExpectedPhysicalHash
	}
	return ""
}

// RetryUploadedCleanup verifies that the publication is still visible before
// deleting derived thumbnails or the previous immutable object. Reference
// uncertainty is a retryable/pending outcome, never permission to delete.
func (s *Service) RetryUploadedCleanup(ctx context.Context, intent UploadedCleanupIntent) UploadedCleanupResult {
	logicPath, err := logicpath.Parse(intent.LogicPath)
	if err != nil {
		return UploadedCleanupResult{Pending: true, Errors: []error{err}}
	}
	publishedKey := strings.TrimSpace(intent.PublishedPhysicalHash)
	if logicPath == "" || publishedKey == "" {
		return UploadedCleanupResult{Pending: true, Errors: []error{errors.New("valid published path and object are required")}}
	}
	published, found, err := s.store.Find(ctx, logicPath)
	if err != nil {
		return UploadedCleanupResult{Pending: true, Errors: []error{fmt.Errorf("verify published mapping: %w", err)}}
	}
	if !found || published.IsDirectory || published.PhysicalHash != publishedKey {
		return UploadedCleanupResult{Pending: true, Errors: []error{fmt.Errorf("%w: published mapping changed", db.ErrPathConflict)}}
	}

	result := UploadedCleanupResult{}
	thumbnails, detachErr := s.store.DetachThumbnails(ctx, []int{published.ID})
	if detachErr != nil {
		result.Pending = true
		result.Errors = append(result.Errors, fmt.Errorf("detach stale thumbnail: %w", detachErr))
	} else {
		for _, thumbnail := range thumbnails {
			if deleteErr := s.thumbnailObjects.Delete(ctx, thumbnail.PhysicalHash); deleteErr != nil {
				result.Pending = true
				result.Errors = append(result.Errors, fmt.Errorf("delete stale thumbnail %s: %w", thumbnail.ID, deleteErr))
			}
		}
	}

	previous := strings.TrimSpace(intent.PreviousPhysicalHash)
	if previous == "" || previous == publishedKey {
		return result
	}
	referenced, referenceErr := s.store.IsObjectReferenced(ctx, previous, logicPath)
	if referenceErr != nil {
		result.Pending = true
		result.Errors = append(result.Errors, fmt.Errorf("check previous object references: %w", referenceErr))
		return result
	}
	if referenced {
		result.Pending = true
		result.Errors = append(result.Errors, fmt.Errorf("previous object %s is still referenced", previous))
		return result
	}
	if deleteErr := s.objects.Delete(ctx, previous); deleteErr != nil {
		result.Pending = true
		result.Errors = append(result.Errors, fmt.Errorf("delete previous object %s: %w", previous, deleteErr))
	}
	return result
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
