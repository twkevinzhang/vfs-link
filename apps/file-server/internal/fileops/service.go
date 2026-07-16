package fileops

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type Service struct {
	store   db.Store
	objects blob.Store
}

func New(store db.Store, objects blob.Store) *Service {
	return &Service{store: store, objects: objects}
}

func (s *Service) Move(ctx context.Context, paths []string, destination string) ([]db.FileRecord, error) {
	return s.store.BatchMove(ctx, paths, destination)
}

func (s *Service) Trash(ctx context.Context, paths []string) ([]db.FileRecord, error) {
	items := make([]db.TrashPath, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			items = append(items, db.TrashPath{Path: path, TrashID: uuid.NewString()})
		}
	}
	return s.store.TrashPaths(ctx, items)
}

func (s *Service) ListTrash(ctx context.Context) ([]db.FileRecord, error) {
	return s.store.ListTrash(ctx)
}
func (s *Service) Restore(ctx context.Context, ids []string) ([]db.FileRecord, error) {
	return s.store.RestoreTrash(ctx, ids)
}

func (s *Service) DeletePermanently(ctx context.Context, ids []string) (int64, error) {
	records, err := s.store.ClaimTrash(ctx, ids)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
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
		if record.IsDirectory || record.PhysicalHash == "" || seen[record.PhysicalHash] {
			continue
		}
		if err := s.objects.Delete(ctx, record.PhysicalHash); err != nil {
			return 0, fmt.Errorf("delete object %s: %w", record.PhysicalHash, err)
		}
		seen[record.PhysicalHash] = true
	}
	deleted, err := s.store.DeleteTrash(ctx, claimedIDs)
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
