package main

import (
	"context"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func cleanupExpiredThumbnailObjects(ctx context.Context, store db.Store, thumbnailObjects blob.Store, now time.Time) (int, error) {
	collector, ok := store.(db.ThumbnailGarbageCollector)
	if !ok {
		return 0, nil
	}
	return collector.CleanupExpiredThumbnails(ctx, now, func(deleteCtx context.Context, record db.ThumbnailRecord) error {
		return thumbnailObjects.Delete(deleteCtx, record.PhysicalHash)
	})
}
