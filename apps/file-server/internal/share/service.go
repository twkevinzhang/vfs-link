package share

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
)

const (
	StatusDraft             = "draft"
	StatusUploading         = "uploading"
	StatusCompleted         = "completed"
	StatusNotified          = "notified"
	StatusFailed            = "failed"
	StatusNotifyFailed      = "notification_failed"
	StatusEmailFailedLegacy = "email_failed"
	shareJobTimeout         = 55 * time.Minute
	shareLeaseDuration      = 56 * time.Minute
	DispatchNone            = "none"
	DispatchPending         = "pending"
	Dispatching             = "dispatching"
	DispatchDispatched      = "dispatched"
	DispatchFailed          = "dispatch_failed"
	DispatchPaused          = "dispatch_paused"
)

var ErrDispatchPending = errors.New("share dispatch is durably pending")

type Service struct {
	cfg         config.Config
	store       MetadataStore
	objects     blob.Store
	logger      *slog.Logger
	dispatcher  Dispatcher
	relay       *Relay
	now         func() time.Time
	uploader    ShareUploader
	notifier    ShareNotifier
	workers     chan struct{}
	workerCtx   context.Context
	stopWorkers context.CancelFunc
}

// MetadataStore is the share package's persistence boundary. Backends do not
// need to expose unrelated file, upload, thumbnail, or DAV operations here.
type MetadataStore interface {
	Find(context.Context, string) (db.FileRecord, bool, error)
	CreateShare(context.Context, db.ShareRecord) (db.ShareRecord, error)
	FindShare(context.Context, string) (db.ShareRecord, bool, error)
	RequestShareJob(context.Context, string, string, time.Time) (db.ShareRecord, bool, error)
	ClaimPendingShareDispatch(context.Context, string, time.Time, time.Time, int) ([]db.ShareRecord, error)
	MarkShareDispatched(context.Context, string, string, time.Time) error
	RetryShareDispatch(context.Context, string, string, time.Time, string) error
	FailShareDispatch(context.Context, string, string, string) error
	ClaimShareJob(context.Context, string, string, time.Time) (db.ShareRecord, bool, error)
	ReleaseShareJob(context.Context, string, string) error
	MarkShareUploadedBy(context.Context, string, string) (db.ShareRecord, error)
	MarkShareNotifiedBy(context.Context, string, string) (db.ShareRecord, error)
	MarkShareFailedBy(context.Context, string, string, string, string) (db.ShareRecord, error)
	StopShareRedelivery(context.Context, string, string) error
}

type ShareUploader interface {
	UploadShare(context.Context, db.ShareRecord) error
}

type ShareNotifier interface {
	NotifyShare(context.Context, db.ShareRecord) error
}

type uploadShareFunc func(context.Context, db.ShareRecord) error

func (f uploadShareFunc) UploadShare(ctx context.Context, record db.ShareRecord) error {
	return f(ctx, record)
}

type notifyShareFunc func(context.Context, db.ShareRecord) error

func (f notifyShareFunc) NotifyShare(ctx context.Context, record db.ShareRecord) error {
	return f(ctx, record)
}

type Option func(*Service)

func WithDispatcher(dispatcher Dispatcher) Option {
	return func(service *Service) { service.dispatcher = dispatcher }
}

func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

func WithUploader(uploader ShareUploader) Option {
	return func(service *Service) { service.uploader = uploader }
}

func WithNotifier(notifier ShareNotifier) Option {
	return func(service *Service) { service.notifier = notifier }
}

func NewService(cfg config.Config, store MetadataStore, objects blob.Store, logger *slog.Logger, options ...Option) *Service {
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	service := &Service{
		cfg:         cfg,
		store:       store,
		objects:     objects,
		logger:      logger,
		now:         func() time.Time { return time.Now().UTC() },
		workers:     make(chan struct{}, 8),
		workerCtx:   workerCtx,
		stopWorkers: stopWorkers,
	}
	for _, option := range options {
		option(service)
	}
	if service.uploader == nil {
		service.uploader = uploadShareFunc(service.upload)
	}
	if service.notifier == nil {
		service.notifier = notifyShareFunc(service.sendNotification)
	}
	if service.dispatcher == nil {
		service.dispatcher = dispatcherFunc(service.dispatchLocal)
	}
	service.relay = NewRelay(store, service.dispatcher, logger, WithRelayClock(service.now))
	return service
}

func (s *Service) CreateDraft(ctx context.Context, logicPath string) (db.ShareRecord, error) {
	if strings.TrimSpace(s.cfg.ShareGCSBucket) == "" {
		return db.ShareRecord{}, errors.New("SHARE_GCS_BUCKET is required")
	}

	logicPath, err := logicpath.Parse(logicPath)
	if err != nil {
		return db.ShareRecord{}, err
	}
	file, found, err := s.store.Find(ctx, logicPath)
	if err != nil {
		return db.ShareRecord{}, err
	}
	if !found || file.IsDirectory {
		return db.ShareRecord{}, db.ErrNotFound
	}

	id := uuid.NewString()
	fileName := path.Base(file.LogicPath)
	objectName := s.destinationObject(id, fileName)
	return s.store.CreateShare(ctx, db.ShareRecord{
		ID:                id,
		LogicPath:         file.LogicPath,
		PhysicalHash:      file.PhysicalHash,
		FileName:          fileName,
		Size:              file.Size,
		DestinationObject: objectName,
		ShareURL:          s.shareURL(objectName),
		Status:            StatusDraft,
	})
}

func (s *Service) Find(ctx context.Context, id string) (db.ShareRecord, bool, error) {
	return s.store.FindShare(ctx, id)
}

func (s *Service) Start(ctx context.Context, id string) (db.ShareRecord, error) {
	record, dispatchNeeded, err := s.store.RequestShareJob(ctx, id, strings.TrimSpace(s.cfg.TelegramChatID), s.now())
	if err != nil {
		return db.ShareRecord{}, err
	}
	if record.Status == StatusNotified || !dispatchNeeded {
		return record, nil
	}
	if err := s.relay.DispatchOne(ctx, id); err != nil {
		if current, found, loadErr := s.store.FindShare(ctx, id); loadErr == nil && found {
			record = current
		}
		return record, fmt.Errorf("%w: %v", ErrDispatchPending, err)
	}
	return record, nil
}

func (s *Service) ProcessShareJob(parent context.Context, job Job) error {
	if err := job.Validate(); err != nil {
		return Permanent(err)
	}
	ctx, cancel := context.WithTimeout(parent, shareJobTimeout)
	defer cancel()

	owner := uuid.NewString()
	record, claimed, err := s.store.ClaimShareJob(ctx, job.ShareID, owner, s.now().Add(shareLeaseDuration))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Permanent(err)
		}
		return fmt.Errorf("claim share job: %w", err)
	}
	if !claimed {
		current, found, findErr := s.store.FindShare(ctx, job.ShareID)
		if findErr != nil {
			return fmt.Errorf("load unclaimed share job: %w", findErr)
		}
		if !found {
			return Permanent(db.ErrNotFound)
		}
		if current.Status == StatusNotified {
			return nil
		}
		// Pub/Sub and relay delivery are intentionally at-least-once. An active
		// lease means this is a harmless duplicate and must be ACKed.
		return nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.ReleaseShareJob(releaseCtx, job.ShareID, owner); err != nil {
			s.logger.Error("release share job lease", "share_id", job.ShareID, "error", err)
		}
	}()

	if record.Status == StatusNotified {
		return nil
	}
	if record.CompletedAt == nil {
		if err := s.uploader.UploadShare(ctx, record); err != nil {
			s.logger.Error("share upload failed", "share_id", job.ShareID, "error", err)
			_, _ = s.store.MarkShareFailedBy(context.Background(), job.ShareID, owner, StatusFailed, err.Error())
			if IsPermanent(err) {
				_ = s.store.StopShareRedelivery(context.Background(), job.ShareID, owner)
			}
			return err
		}

		record, err = s.store.MarkShareUploadedBy(context.Background(), job.ShareID, owner)
		if err != nil {
			return fmt.Errorf("mark share uploaded: %w", err)
		}
	}

	if strings.TrimSpace(s.cfg.TelegramBotToken) == "" || strings.TrimSpace(s.cfg.TelegramChatID) == "" {
		err := errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
		_, _ = s.store.MarkShareFailedBy(context.Background(), job.ShareID, owner, StatusNotifyFailed, err.Error())
		_ = s.store.StopShareRedelivery(context.Background(), job.ShareID, owner)
		return Permanent(err)
	}
	if err := s.notifier.NotifyShare(ctx, record); err != nil {
		s.logger.Error("share telegram notification failed", "share_id", job.ShareID, "chat_id", s.cfg.TelegramChatID, "error", err)
		_, _ = s.store.MarkShareFailedBy(context.Background(), job.ShareID, owner, StatusNotifyFailed, err.Error())
		if IsPermanent(err) {
			_ = s.store.StopShareRedelivery(context.Background(), job.ShareID, owner)
		}
		return err
	}
	if _, err := s.store.MarkShareNotifiedBy(context.Background(), job.ShareID, owner); err != nil {
		return fmt.Errorf("mark share notified: %w", err)
	}
	return nil
}

func (s *Service) dispatchLocal(_ context.Context, job Job) error {
	select {
	case s.workers <- struct{}{}:
		go func() {
			defer func() { <-s.workers }()
			if err := s.ProcessShareJob(s.workerCtx, job); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("process local share job", "share_id", job.ShareID, "error", err)
			}
		}()
		return nil
	default:
		return errors.New("local share worker queue is full")
	}
}

// RunRelay recovers durable pending, expired dispatch leases, and stale
// accepted jobs. It is bounded per pass and exits when ctx is cancelled.
func (s *Service) RunRelay(ctx context.Context) error {
	defer s.stopWorkers()
	return s.relay.Run(ctx)
}

func (s *Service) Wait(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(s.workers) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) upload(ctx context.Context, record db.ShareRecord) error {
	metadata := map[string]string{
		"vfs-link-logic-path": record.LogicPath,
		"vfs-link-share-id":   record.ID,
	}
	if copier, ok := s.objects.(blob.GCSObjectCopier); ok {
		if err := copier.CopyToGCS(ctx, record.PhysicalHash, s.cfg.ShareGCSBucket, record.DestinationObject, metadata); err != nil {
			return fmt.Errorf("copy object in GCS: %w", err)
		}
		return nil
	}

	reader, err := s.objects.NewReader(ctx, record.PhysicalHash)
	if err != nil {
		return fmt.Errorf("open source object: %w", err)
	}
	defer reader.Close()

	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("create GCS client: %w", err)
	}
	defer client.Close()

	writer := client.Bucket(s.cfg.ShareGCSBucket).Object(record.DestinationObject).NewWriter(ctx)
	writer.ContentType = "application/octet-stream"
	writer.Metadata = metadata
	if _, err := io.Copy(writer, reader); err != nil {
		_ = writer.Close()
		return fmt.Errorf("upload to GCS: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish GCS upload: %w", err)
	}
	return nil
}

func (s *Service) sendNotification(ctx context.Context, record db.ShareRecord) error {
	botToken := strings.TrimSpace(s.cfg.TelegramBotToken)
	chatID := strings.TrimSpace(s.cfg.TelegramChatID)
	if botToken == "" || chatID == "" {
		return errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
	}

	message := strings.Join([]string{
		"vfs-link shared file",
		fmt.Sprintf("File: %s", record.FileName),
		fmt.Sprintf("Path: %s", record.LogicPath),
		fmt.Sprintf("Size: %s", formatBytes(record.Size)),
		fmt.Sprintf("Link: %s", record.ShareURL),
	}, "\n")

	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", message)
	form.Set("disable_web_page_preview", "true")

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("send telegram message: %s", redactTelegramToken(err.Error(), botToken))
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		err := fmt.Errorf(
			"telegram sendMessage status %d: %s",
			response.StatusCode,
			redactTelegramToken(strings.TrimSpace(string(body)), botToken),
		)
		if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < 500 {
			return Permanent(err)
		}
		return err
	}
	return nil
}

func redactTelegramToken(value, botToken string) string {
	if botToken == "" {
		return value
	}
	return strings.ReplaceAll(value, botToken, "<redacted>")
}

func (s *Service) destinationObject(id, fileName string) string {
	prefix := strings.Trim(strings.TrimSpace(s.cfg.ShareGCSPrefix), "/")
	parts := []string{}
	if prefix != "" {
		parts = append(parts, prefix)
	}
	parts = append(parts, time.Now().UTC().Format("2006/01/02"), id, fileName)
	return path.Join(parts...)
}

func (s *Service) shareURL(objectName string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(s.cfg.SharePublicURL), "/")
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://storage.googleapis.com/%s", s.cfg.ShareGCSBucket)
	}
	escaped := escapeObjectName(objectName)
	return baseURL + "/" + escaped
}

func escapeObjectName(objectName string) string {
	parts := strings.Split(objectName, "/")
	for idx, part := range parts {
		parts[idx] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for value := size / unit; value >= unit; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
