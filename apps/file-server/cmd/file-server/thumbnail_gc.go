package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

const (
	thumbnailGCInitialDelay = 10 * time.Minute
	thumbnailGCInterval     = 24 * time.Hour
)

// startThumbnailGarbageCollector keeps derived WebP objects for the metadata
// grace period, then runs the intentionally O(total thumbnails) sweep outside
// request handling. A cold instance waits before its first sweep so startup and
// canary traffic never pay that scan synchronously.
func startThumbnailGarbageCollector(ctx context.Context, store db.Store, objects blob.Store, logger *slog.Logger) {
	if _, ok := store.(db.ThumbnailGarbageCollector); !ok {
		return
	}
	go func() {
		timer := time.NewTimer(thumbnailGCInitialDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		for {
			cleanupCtx, cancel := context.WithTimeout(ctx, time.Hour)
			deleted, err := cleanupExpiredThumbnailObjects(cleanupCtx, store, objects, time.Now().UTC())
			cancel()
			if err != nil {
				logger.Warn("thumbnail garbage collection failed", "error", err)
			} else if deleted > 0 {
				logger.Info("thumbnail garbage collection completed", "deleted", deleted)
			}

			timer.Reset(thumbnailGCInterval)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}

func cleanupExpiredThumbnailObjects(ctx context.Context, store db.Store, objects blob.Store, now time.Time) (int, error) {
	collector, ok := store.(db.ThumbnailGarbageCollector)
	if !ok {
		return 0, nil
	}
	return collector.CleanupExpiredThumbnails(ctx, now, func(deleteCtx context.Context, record db.ThumbnailRecord) error {
		return objects.Delete(deleteCtx, record.PhysicalHash)
	})
}
