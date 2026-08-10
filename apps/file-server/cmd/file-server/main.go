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
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	ftpdriver "github.com/twkevinzhang/vfs-link/apps/file-server/internal/ftp"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/httpauth"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/share"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/upload"
	davserver "github.com/twkevinzhang/vfs-link/apps/file-server/internal/webdav"
)

const (
	uploadRecoveryInterval = 30 * time.Second
	uploadRecoveryBatch    = 50
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
	if cfg.DatabaseDriver == "json" && cfg.MetadataPrefix == "_vfs-link-v4" {
		reducerCtx, cancelReducer := context.WithCancel(ctx)
		reducerDone := make(chan struct{})
		go func() {
			defer close(reducerDone)
			err := db.RunTreeDerivedReducerLoop(reducerCtx, store, db.TreeDerivedReducerLoopOptions{
				Interval: cfg.MetadataReducerInterval,
				OnPass: func(result db.TreeDerivedReduceResult) {
					if result.Discovered > 0 || result.Applied > 0 || result.Replayed > 0 || result.Pending > 0 {
						logger.Info("reduced derived metadata", "discovered", result.Discovered, "applied", result.Applied, "replayed", result.Replayed, "pending", result.Pending)
					}
				},
				OnError: func(err error) {
					if errors.Is(err, db.ErrDerivedReducerBusy) {
						logger.Debug("derived metadata reducer lease is held by another instance")
						return
					}
					logger.Error("derived metadata reducer pass failed", "error", err)
				},
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("derived metadata reducer loop stopped", "error", err)
			}
		}()
		defer func() {
			cancelReducer()
			select {
			case <-reducerDone:
			case <-time.After(10 * time.Second):
				logger.Warn("derived metadata reducer did not stop within 10 seconds")
			}
		}()
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

	thumbnailObjects, err := blob.NewStore(ctx, blob.StoreConfig{
		Driver:    cfg.ThumbnailStorageDriver,
		LocalRoot: cfg.ThumbnailLocalRoot,
		GCSBucket: cfg.ThumbnailGCSBucket,
	})
	if err != nil {
		return fmt.Errorf("initialize thumbnail storage: %w", err)
	}
	defer thumbnailObjects.Close()
	logger.Info("initialized thumbnail storage", "driver", thumbnailObjects.Driver(), "root", thumbnailObjects.Root())
	fileService := fileops.New(store, objects, thumbnailObjects)

	if len(cfg.CommandArgs) > 0 {
		switch cfg.CommandArgs[0] {
		case "rebuild-mapping":
			return rebuildMapping(ctx, cfg, store, objects, logger)
		}
	}
	if err := ensureNoLegacyUploadSessions(ctx, store); err != nil {
		return err
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
	uploadService := upload.NewWithBlobAndPublisher(store, uploadPublisher{store: store, files: fileService}, objects,
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
		httpHandler.Handle(cfg.WebDAVPath, davserver.NewWithCommands(davserver.Config{
			Prefix: cfg.WebDAVPath, User: cfg.WebDAVUser, Pass: cfg.WebDAVPass,
			LockTimeout: cfg.WebDAVLockTimeout, TrustForwardedHeaders: cfg.WebDAVTrustProxy,
		}, store, objects, fileService, logger))
		logger.Info("WebDAV enabled", "path", cfg.WebDAVPath)
	}
	publicHandler := api.New(store, objects, thumbnailObjects, shareService, cfg.WebStaticRoot, cfg.WebBasePath, uploadService).
		SetFileService(fileService).
		SetDriftEnabled(cfg.DriftEnabled).
		SetCORSOrigins(strings.Split(cfg.HTTPCORSOrigins, ",")).Handler()
	httpHandler.Handle("/", httpauth.Basic(cfg.HTTPBasicAuth, cfg.HTTPBasicUser, cfg.HTTPBasicPass, publicHandler))
	apiServer := &http.Server{
		Addr:              cfg.HTTPListenAddr(),
		Handler:           maintenanceMode(cfg.MaintenanceMode, httpHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 3)
	relayCtx, cancelRelay := context.WithCancel(ctx)
	relayDone := make(chan error, 1)
	go func() { relayDone <- shareService.RunRelay(relayCtx) }()
	defer cancelRelay()
	uploadRecoveryCtx, cancelUploadRecovery := context.WithCancel(ctx)
	uploadRecoveryDone := make(chan error, 1)
	go func() {
		err := uploadService.RunRecovery(uploadRecoveryCtx, uploadRecoveryInterval, uploadRecoveryBatch)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("upload recovery loop stopped", "error", err)
			errCh <- fmt.Errorf("upload recovery loop stopped: %w", err)
		}
		uploadRecoveryDone <- err
	}()

	var ftpServer *ftpserver.FtpServer
	runningServers := 1
	if cfg.FTPEnabled {
		driver := ftpdriver.NewMainDriverWithCommands(cfg, store, objects, logger, fileService, 0)
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
		logger.Info("starting HTTP server", "listen", cfg.HTTPListenAddr(), "storage_root", objects.Root(), "thumbnail_storage_root", thumbnailObjects.Root(), "web_static_root", cfg.WebStaticRoot, "web_base_path", cfg.WebBasePath)
		if err := apiServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		cancelRelay()
		cancelUploadRecovery()
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
		if err := fileService.WaitOperations(shutdownCtx); err != nil {
			return fmt.Errorf("wait for file operations: %w", err)
		}
		if err := waitShareService(shutdownCtx, shareService, relayDone); err != nil {
			return err
		}
		if err := waitUploadRecovery(shutdownCtx, uploadRecoveryDone); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		cancelRelay()
		cancelUploadRecovery()
		if ftpServer != nil {
			ftpServer.Stop()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = apiServer.Shutdown(shutdownCtx)
		if waitErr := fileService.WaitOperations(shutdownCtx); waitErr != nil && err == nil {
			return fmt.Errorf("wait for file operations: %w", waitErr)
		}
		if waitErr := waitShareService(shutdownCtx, shareService, relayDone); waitErr != nil && err == nil {
			return waitErr
		}
		if waitErr := waitUploadRecovery(shutdownCtx, uploadRecoveryDone); waitErr != nil && err == nil {
			return waitErr
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}

func ensureNoLegacyUploadSessions(ctx context.Context, store db.Store) error {
	const limit = 501
	records, err := store.ListNonterminalUploads(ctx, limit)
	if err != nil {
		return fmt.Errorf("inspect legacy upload sessions: %w", err)
	}
	legacy := 0
	for _, record := range records {
		if !objectkey.IsUploadGeneration(record.LogicPath, record.ID, record.PhysicalHash) {
			legacy++
		}
	}
	if legacy > 0 {
		return fmt.Errorf("refusing to start with %d nonterminal legacy upload session(s); drain or cancel them with the previous release before rollout", legacy)
	}
	if len(records) == limit {
		return errors.New("refusing to start: more than 500 nonterminal upload sessions require rollout preflight")
	}
	return nil
}

func waitUploadRecovery(ctx context.Context, done <-chan error) error {
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("upload recovery: %w", err)
		}
		return nil
	case <-ctx.Done():
		return errors.New("upload recovery did not stop before shutdown deadline")
	}
}

func waitShareService(ctx context.Context, service *share.Service, relayDone <-chan error) error {
	select {
	case err := <-relayDone:
		if err != nil {
			return fmt.Errorf("share relay: %w", err)
		}
	case <-ctx.Done():
		return errors.New("share relay did not stop before shutdown deadline")
	}
	if err := service.Wait(ctx); err != nil {
		return fmt.Errorf("wait for share workers: %w", err)
	}
	return nil
}

type uploadPublisher struct {
	store db.Store
	files *fileops.Service
}

func (p uploadPublisher) FindFile(ctx context.Context, logicPath string) (upload.File, bool, error) {
	record, found, err := p.store.Find(ctx, logicPath)
	return upload.File{
		ID:           record.ID,
		PhysicalHash: record.PhysicalHash,
		IsDirectory:  record.IsDirectory,
		Size:         record.Size,
		UpdatedAt:    record.UpdatedAt,
	}, found, err
}

func (p uploadPublisher) EnsureDirectory(ctx context.Context, logicPath string) error {
	_, err := p.files.CreateDirectory(ctx, logicPath)
	return err
}

func (p uploadPublisher) IsUploadGenerationReferenced(ctx context.Context, physicalHash string) (bool, error) {
	return p.store.IsObjectReferenced(ctx, physicalHash, "")
}

func (p uploadPublisher) ReplaceFile(ctx context.Context, logicPath, physicalHash string, size int64, expected *string, snapshot *upload.FileSnapshot, absent bool) (upload.PublishResult, bool, error) {
	intent := fileops.PublishIntent{
		LogicPath: logicPath, PhysicalHash: physicalHash, Size: size,
		ExpectedPhysicalHash: expected, RequireAbsent: absent, DeferCleanup: true,
	}
	if snapshot != nil {
		intent.ExpectedPhysicalHash = nil
		intent.ExpectedSnapshot = &db.FileSnapshot{
			ID: snapshot.ID, UpdatedAt: snapshot.UpdatedAt, PhysicalHash: snapshot.PhysicalHash,
		}
	}
	result, err := p.files.PublishUploaded(ctx, intent)
	if errors.Is(err, db.ErrPathConflict) {
		return upload.PublishResult{}, false, nil
	}
	cleanupError := ""
	if len(result.CleanupErrors) > 0 {
		cleanupError = errors.Join(result.CleanupErrors...).Error()
	}
	return upload.PublishResult{
		PreviousPhysicalHash: result.PreviousObject,
		CleanupPending:       result.CleanupPending,
		CleanupError:         cleanupError,
	}, err == nil, err
}

func (p uploadPublisher) RetryUploadCleanup(ctx context.Context, intent upload.CleanupIntent) (upload.CleanupResult, error) {
	result := p.files.RetryUploadedCleanup(ctx, fileops.UploadedCleanupIntent{
		LogicPath: intent.LogicPath, PublishedPhysicalHash: intent.PublishedPhysicalHash,
		PreviousPhysicalHash: intent.PreviousPhysicalHash,
	})
	if len(result.Errors) == 0 {
		return upload.CleanupResult{Pending: result.Pending}, nil
	}
	err := errors.Join(result.Errors...)
	return upload.CleanupResult{Pending: result.Pending, Error: err.Error()}, err
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
		if cfg.MetadataPrefix == "_vfs-link-v4" {
			options := db.TreeV4Options{
				ShardCount:      cfg.MetadataShardCount,
				ReducerInterval: cfg.MetadataReducerInterval,
				MutationMode:    cfg.MetadataMutationMode,
			}
			switch cfg.MetadataStorageDriver {
			case "local":
				return db.NewTreeLocalV4(cfg.MetadataLocalRoot, cfg.MetadataPrefix, options)
			case "gcs":
				return db.NewTreeGCSV4(ctx, cfg.MetadataGCSBucket, cfg.MetadataPrefix, options)
			default:
				return nil, fmt.Errorf("unsupported METADATA_STORAGE_DRIVER %q", cfg.MetadataStorageDriver)
			}
		}
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
		logicPath := strings.Trim(strings.TrimSpace(object.Name), "/")
		if logicPath == "" {
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
