package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fclairamb/ftpserverlib"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/api"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	ftpdriver "github.com/twkevinzhang/vfs-link/apps/file-server/internal/ftp"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/httpauth"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/share"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/upload"
	davserver "github.com/twkevinzhang/vfs-link/apps/file-server/internal/webdav"
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

	store, err := newMetadataStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}

	objects, err := blob.NewStore(ctx, blob.StoreConfig{
		Driver:    cfg.StorageDriver,
		LocalRoot: cfg.LocalStorageRoot,
		GCSBucket: cfg.GCSBucket,
	})
	if err != nil {
		return fmt.Errorf("initialize object storage: %w", err)
	}
	defer objects.Close()
	logger.Info("initialized object storage", "driver", objects.Driver(), "root", objects.Root())

	if len(cfg.CommandArgs) > 0 && cfg.CommandArgs[0] == "rebuild-mapping" {
		return rebuildMapping(ctx, cfg, store, objects, logger)
	}

	var shareOptions []share.Option
	if cfg.PubSubDriver == "pubsub" {
		dispatcher, err := share.NewPubSubDispatcher(ctx, cfg.GCPProjectID, cfg.PubSubTopic)
		if err != nil {
			return fmt.Errorf("initialize Pub/Sub dispatcher: %w", err)
		}
		shareOptions = append(shareOptions, share.WithDispatcher(dispatcher))
	}
	shareService := share.NewService(cfg, store, objects, logger, shareOptions...)
	uploadService := upload.NewWithBlob(store, objects,
		upload.WithTTL(cfg.UploadSessionTTL),
		upload.WithMaxBytes(cfg.UploadMaxBytes),
	)
	httpHandler := http.NewServeMux()
	if cfg.PubSubDriver == "pubsub" {
		pushHandler, err := share.NewPushHandler(share.PushHandlerConfig{
			Audience: cfg.PubSubAudience, ServiceAccountEmail: cfg.PubSubPushEmail,
		}, shareService, nil, logger)
		if err != nil {
			return fmt.Errorf("initialize Pub/Sub push handler: %w", err)
		}
		httpHandler.Handle("/internal/pubsub/shares", pushHandler)
	}
	if cfg.WebDAVEnabled {
		httpHandler.Handle(cfg.WebDAVPath, davserver.New(davserver.Config{
			Prefix: cfg.WebDAVPath, User: cfg.WebDAVUser, Pass: cfg.WebDAVPass,
			LockTimeout: cfg.WebDAVLockTimeout, TrustForwardedHeaders: cfg.WebDAVTrustProxy,
		}, store, objects, logger))
		logger.Info("WebDAV enabled", "path", cfg.WebDAVPath)
	}
	publicHandler := api.New(store, objects, shareService, cfg.WebStaticRoot, cfg.WebBasePath, uploadService).
		SetDriftEnabled(cfg.DriftEnabled).
		SetCORSOrigins(strings.Split(cfg.HTTPCORSOrigins, ",")).Handler()
	httpHandler.Handle("/", httpauth.Basic(cfg.HTTPBasicAuth, cfg.HTTPBasicUser, cfg.HTTPBasicPass, publicHandler))
	apiServer := &http.Server{
		Addr:              cfg.HTTPListenAddr(),
		Handler:           maintenanceMode(cfg.MaintenanceMode, httpHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	var ftpServer *ftpserver.FtpServer
	runningServers := 1
	if cfg.FTPEnabled {
		driver := ftpdriver.NewMainDriver(cfg, store, objects, logger)
		ftpServer = ftpserver.NewFtpServer(driver)
		runningServers++
		go func() {
			logger.Info("starting FTP server",
				"listen", cfg.ListenAddr(),
				"pasv_url", cfg.FTPPasvURL,
				"pasv_min", cfg.FTPPasvMin,
				"pasv_max", cfg.FTPPasvMax,
			)
			errCh <- ftpServer.ListenAndServe()
		}()
	} else {
		logger.Info("FTP server disabled")
	}
	go func() {
		logger.Info("starting HTTP server", "listen", cfg.HTTPListenAddr(), "storage_root", objects.Root(), "web_static_root", cfg.WebStaticRoot, "web_base_path", cfg.WebBasePath)
		if err := apiServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		if ftpServer != nil {
			ftpServer.Stop()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := apiServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		for range runningServers {
			select {
			case err := <-errCh:
				if err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
			case <-shutdownCtx.Done():
				return errors.New("file server did not stop within 10 seconds")
			}
		}
		return nil
	case err := <-errCh:
		if ftpServer != nil {
			ftpServer.Stop()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = apiServer.Shutdown(shutdownCtx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}

func maintenanceMode(enabled bool, next http.Handler) http.Handler {
	if !enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"maintenance mode: metadata mutations are temporarily disabled"}`))
		}
	})
}

func newMetadataStore(ctx context.Context, cfg config.Config) (db.Store, error) {
	switch cfg.DatabaseDriver {
	case "postgres":
		return db.NewPostgres(ctx, cfg.DatabaseURL)
	case "json":
		switch cfg.MetadataStorageDriver {
		case "local":
			return db.NewTreeLocal(cfg.MetadataLocalRoot, cfg.MetadataPrefix)
		case "gcs":
			return db.NewTreeGCS(ctx, cfg.MetadataGCSBucket, cfg.MetadataPrefix)
		default:
			return nil, fmt.Errorf("unsupported METADATA_STORAGE_DRIVER %q", cfg.MetadataStorageDriver)
		}
	default:
		return nil, fmt.Errorf("unsupported DB_DRIVER %q", cfg.DatabaseDriver)
	}
}

func rebuildMapping(ctx context.Context, cfg config.Config, store db.Store, objects blob.Store, logger *slog.Logger) error {
	if !cfg.AssumeYes {
		logger.Warn("rebuilding mapping table from active object store", "driver", objects.Driver(), "root", objects.Root())
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
		return fmt.Errorf("list objects: %w", err)
	}

	logger.Info("processing objects", "count", len(objectList))
	for idx, object := range objectList {
		if strings.HasPrefix(strings.TrimLeft(object.Name, "/"), "_vfs-link/") {
			continue
		}
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
