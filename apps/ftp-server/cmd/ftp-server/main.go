package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fclairamb/ftpserverlib"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/db"
	ftpdriver "github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/ftp"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/gcs"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}

	objects, err := gcs.New(ctx, cfg.GCSBucket)
	if err != nil {
		return err
	}
	defer objects.Close()

	if len(cfg.CommandArgs) > 0 && cfg.CommandArgs[0] == "rebuild-mapping" {
		return rebuildMapping(ctx, cfg, store, objects, logger)
	}

	driver := ftpdriver.NewMainDriver(cfg, store, objects, logger)
	server := ftpserver.NewFtpServer(driver)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting FTP server",
			"listen", cfg.ListenAddr(),
			"pasv_url", cfg.FTPPasvURL,
			"pasv_min", cfg.FTPPasvMin,
			"pasv_max", cfg.FTPPasvMax,
		)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		server.Stop()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		case <-time.After(10 * time.Second):
			return errors.New("FTP server did not stop within 10 seconds")
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func rebuildMapping(ctx context.Context, cfg config.Config, store *db.Store, objects *gcs.Client, logger *slog.Logger) error {
	if !cfg.AssumeYes {
		logger.Warn("rebuilding mapping table from GCS bucket content", "bucket", cfg.GCSBucket)
		for i := 5; i > 0; i-- {
			logger.Info("starting soon", "seconds", i)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}

	objectList, err := objects.List(ctx)
	if err != nil {
		return fmt.Errorf("list GCS objects: %w", err)
	}

	logger.Info("processing GCS objects", "count", len(objectList))
	for idx, object := range objectList {
		isDir := strings.HasSuffix(object.Name, "/")
		logicPath := "/" + strings.TrimPrefix(strings.TrimSuffix(object.Name, "/"), "/")
		if logicPath == "/" {
			continue
		}

		if isDir {
			err = store.UpsertDirectory(ctx, logicPath)
		} else {
			err = store.UpsertFile(ctx, logicPath, object.Name, object.Size)
		}
		if err != nil {
			return fmt.Errorf("upsert mapping for %s: %w", object.Name, err)
		}

		logger.Info("mapped object", "index", idx+1, "total", len(objectList), "physical", object.Name, "logic", logicPath)
	}

	logger.Info("mapping rebuild completed", "count", len(objectList))
	return nil
}
