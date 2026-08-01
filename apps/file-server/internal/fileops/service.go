package fileops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type Service struct {
	store      db.Store
	objects    blob.Store
	mu         sync.Mutex
	running    map[string]struct{}
	idle       chan struct{}
	resumeDone chan struct{}
}

func New(store db.Store, objects blob.Store) *Service {
	idle := make(chan struct{})
	close(idle)
	service := &Service{store: store, objects: objects, running: make(map[string]struct{}), idle: idle, resumeDone: make(chan struct{})}
	service.resumeOperations()
	return service
}

type MoveResult struct {
	Records   []db.FileRecord
	Operation *db.OperationRecord
}

type RenameResult struct {
	Records   []db.FileRecord
	Operation *db.OperationRecord
}

type RecordsResult struct {
	Records   []db.FileRecord
	Operation *db.OperationRecord
}

type DeleteResult struct {
	Deleted   int64
	Operation *db.OperationRecord
}

type trashOperationStore interface {
	CreateTrashOperation(context.Context, []db.TrashPath) (db.OperationRecord, error)
}

type restoreOperationStore interface {
	CreateRestoreOperation(context.Context, []string) (db.OperationRecord, error)
}

type deleteTrashOperationStore interface {
	CreateDeleteTrashOperation(context.Context, []string) (db.OperationRecord, error)
}

type deleteTrashOperationRunner interface {
	RunDeleteTrashOperation(
		context.Context,
		string,
		func(context.Context, []string, func(int, int) error) (int64, error),
	) (db.OperationRecord, error)
}

func (s *Service) Move(ctx context.Context, paths []string, destination string) (MoveResult, error) {
	if err := s.waitForResume(ctx); err != nil {
		return MoveResult{}, err
	}
	operations, supportsOperations := s.store.(db.TreeOperationStore)
	if supportsOperations {
		containsDirectory := false
		for _, logicPath := range paths {
			record, found, err := s.store.Find(ctx, logicPath)
			if err != nil {
				return MoveResult{}, err
			}
			if !found {
				return MoveResult{}, fmt.Errorf("%w: %s", db.ErrNotFound, logicPath)
			}
			containsDirectory = containsDirectory || record.IsDirectory
		}
		if containsDirectory {
			operation, err := operations.CreateMoveOperation(ctx, paths, destination)
			if err != nil {
				return MoveResult{}, err
			}
			s.kickOperation(operation.ID)
			return MoveResult{Operation: &operation}, nil
		}
	}

	records, err := s.store.BatchMove(ctx, paths, destination)
	return MoveResult{Records: records}, err
}

// Rename changes one active item's final path. Tree-backed directory renames
// run as durable operations because every descendant is an individual object.
func (s *Service) Rename(ctx context.Context, logicPath, name string) (RenameResult, error) {
	if err := s.waitForResume(ctx); err != nil {
		return RenameResult{}, err
	}
	from, to, err := db.RenameTarget(logicPath, name)
	if err != nil {
		return RenameResult{}, err
	}
	record, found, err := s.store.Find(ctx, from)
	if err != nil {
		return RenameResult{}, err
	}
	if !found {
		return RenameResult{}, fmt.Errorf("%w: %s", db.ErrNotFound, from)
	}
	if _, exists, err := s.store.Find(ctx, to); err != nil {
		return RenameResult{}, err
	} else if exists {
		return RenameResult{}, fmt.Errorf("%w: %s", db.ErrPathConflict, to)
	}
	if operations, ok := s.store.(db.TreeOperationStore); ok && record.IsDirectory {
		operation, err := operations.CreateRenameOperation(ctx, from, name)
		if err != nil {
			return RenameResult{}, err
		}
		s.kickOperation(operation.ID)
		return RenameResult{Operation: &operation}, nil
	}
	if err := s.store.RenamePath(ctx, from, to); err != nil {
		return RenameResult{}, err
	}
	renamed, found, err := s.store.Find(ctx, to)
	if err != nil {
		return RenameResult{}, err
	}
	if !found {
		return RenameResult{}, fmt.Errorf("%w: %s", db.ErrNotFound, to)
	}
	return RenameResult{Records: []db.FileRecord{renamed}}, nil
}

func (s *Service) Operation(ctx context.Context, id string) (db.OperationRecord, bool, error) {
	operations, ok := s.store.(db.TreeOperationStore)
	if !ok {
		return db.OperationRecord{}, false, nil
	}
	operation, found, err := operations.GetOperation(ctx, strings.TrimSpace(id))
	if err == nil && found {
		leaseExpired := operation.LeaseUntil == nil || !operation.LeaseUntil.After(time.Now())
		if operation.Status == "pending" || (operation.Status == "running" && leaseExpired) {
			s.kickOperation(operation.ID)
		}
	}
	return operation, found, err
}

func (s *Service) resumeOperations() {
	go func() {
		defer close(s.resumeDone)
		operations, ok := s.store.(db.TreeOperationStore)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		runnable, err := operations.ListRunnableOperations(ctx)
		if err != nil {
			return
		}
		for _, operation := range runnable {
			s.kickOperation(operation.ID)
		}
	}()
}

func (s *Service) waitForResume(ctx context.Context) error {
	select {
	case <-s.resumeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) kickOperation(id string) {
	operations, ok := s.store.(db.TreeOperationStore)
	if !ok || strings.TrimSpace(id) == "" {
		return
	}
	s.mu.Lock()
	if _, running := s.running[id]; running {
		s.mu.Unlock()
		return
	}
	if len(s.running) == 0 {
		s.idle = make(chan struct{})
	}
	s.running[id] = struct{}{}
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, id)
			if len(s.running) == 0 {
				close(s.idle)
			}
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		operation, found, err := operations.GetOperation(ctx, id)
		if err != nil || !found {
			return
		}
		if operation.Type == "delete-trash" {
			runner, ok := s.store.(deleteTrashOperationRunner)
			if !ok {
				return
			}
			_, _ = runner.RunDeleteTrashOperation(ctx, id, s.deletePermanentlyWithCheckpoint)
			return
		}
		_, _ = operations.RunOperation(ctx, id)
	}()
}

// WaitOperations waits until all operation workers that are currently owned
// by this service have returned. It is primarily useful for graceful shutdown
// and deterministic test cleanup; durable manifests remain the recovery source
// if the process terminates before workers finish.
func (s *Service) WaitOperations(ctx context.Context) error {
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Trash(ctx context.Context, paths []string) (RecordsResult, error) {
	if err := s.waitForResume(ctx); err != nil {
		return RecordsResult{}, err
	}
	items := make([]db.TrashPath, 0, len(paths))
	containsDirectory := false
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			items = append(items, db.TrashPath{Path: path, TrashID: uuid.NewString()})
			if _, supportsOperations := s.store.(db.TreeOperationStore); supportsOperations {
				record, found, err := s.store.Find(ctx, path)
				if err != nil {
					return RecordsResult{}, err
				}
				if !found {
					return RecordsResult{}, fmt.Errorf("%w: %s", db.ErrNotFound, path)
				}
				containsDirectory = containsDirectory || record.IsDirectory
			}
		}
	}
	if operations, ok := s.store.(trashOperationStore); ok && containsDirectory {
		operation, err := operations.CreateTrashOperation(ctx, items)
		if err != nil {
			return RecordsResult{}, err
		}
		s.kickOperation(operation.ID)
		return RecordsResult{Operation: &operation}, nil
	}
	records, err := s.store.TrashPaths(ctx, items)
	return RecordsResult{Records: records}, err
}

func (s *Service) ListTrash(ctx context.Context) ([]db.FileRecord, error) {
	return s.store.ListTrash(ctx)
}
func (s *Service) Restore(ctx context.Context, ids []string) (RecordsResult, error) {
	if err := s.waitForResume(ctx); err != nil {
		return RecordsResult{}, err
	}
	if operations, ok := s.store.(restoreOperationStore); ok {
		roots, err := s.store.ListTrash(ctx)
		if err != nil {
			return RecordsResult{}, err
		}
		wanted := stringSet(ids)
		for _, root := range roots {
			if wanted[root.TrashID] && root.IsDirectory {
				operation, err := operations.CreateRestoreOperation(ctx, ids)
				if err != nil {
					return RecordsResult{}, err
				}
				s.kickOperation(operation.ID)
				return RecordsResult{Operation: &operation}, nil
			}
		}
	}
	records, err := s.store.RestoreTrash(ctx, ids)
	return RecordsResult{Records: records}, err
}

func (s *Service) DeletePermanently(ctx context.Context, ids []string) (DeleteResult, error) {
	if err := s.waitForResume(ctx); err != nil {
		return DeleteResult{}, err
	}
	if operations, ok := s.store.(deleteTrashOperationStore); ok {
		roots, err := s.store.ListTrash(ctx)
		if err != nil {
			return DeleteResult{}, err
		}
		wanted := stringSet(ids)
		for _, root := range roots {
			if len(ids) == 0 || len(ids) > 100 || (root.IsDirectory && wanted[root.TrashID]) {
				operation, err := operations.CreateDeleteTrashOperation(ctx, ids)
				if err != nil {
					return DeleteResult{}, err
				}
				s.kickOperation(operation.ID)
				return DeleteResult{Operation: &operation}, nil
			}
		}
	}
	deleted, err := s.deletePermanently(ctx, ids)
	return DeleteResult{Deleted: deleted}, err
}

func (s *Service) deletePermanently(ctx context.Context, ids []string) (int64, error) {
	return s.deletePermanentlyWithCheckpoint(ctx, ids, nil)
}

func (s *Service) deletePermanentlyWithCheckpoint(ctx context.Context, ids []string, checkpoint func(int, int) error) (int64, error) {
	records, err := s.store.ClaimTrash(ctx, ids)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	physicalHashes := make([]string, 0)
	fileIDs := make([]int, 0)
	claimedIDs := make([]string, 0)
	claimedIDSet := map[string]bool{}
	for _, record := range records {
		if !claimedIDSet[record.TrashID] {
			claimedIDSet[record.TrashID] = true
			claimedIDs = append(claimedIDs, record.TrashID)
		}
	}
	if len(claimedIDs) == 0 {
		return 0, nil
	}
	for _, record := range records {
		if !record.IsDirectory {
			fileIDs = append(fileIDs, record.ID)
		}
		if record.IsDirectory || record.PhysicalHash == "" || seen[record.PhysicalHash] {
			continue
		}
		seen[record.PhysicalHash] = true
		physicalHashes = append(physicalHashes, record.PhysicalHash)
	}
	for index, physicalHash := range physicalHashes {
		if err := s.objects.Delete(ctx, physicalHash); err != nil {
			return 0, fmt.Errorf("delete object %s: %w", physicalHash, err)
		}
		if checkpoint != nil {
			if err := checkpoint(index+1, len(physicalHashes)); err != nil {
				return 0, fmt.Errorf("checkpoint permanent deletion: %w", err)
			}
		}
	}
	orphanedThumbnails, err := s.store.DetachThumbnails(ctx, fileIDs)
	if err != nil {
		return 0, fmt.Errorf("detach thumbnails: %w", err)
	}
	for _, thumbnail := range orphanedThumbnails {
		// Thumbnail objects are derived data. Metadata deletion is authoritative;
		// a transient object cleanup failure must not strand trash forever.
		_ = s.objects.Delete(ctx, thumbnail.PhysicalHash)
	}
	deleted, err := s.store.DeleteTrash(ctx, claimedIDs)
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = true
		}
	}
	return result
}
