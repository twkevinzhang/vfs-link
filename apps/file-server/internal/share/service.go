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
)

type Service struct {
	cfg        config.Config
	store      db.Store
	objects    blob.Store
	logger     *slog.Logger
	dispatcher Dispatcher
}

type Option func(*Service)

func WithDispatcher(dispatcher Dispatcher) Option {
	return func(service *Service) { service.dispatcher = dispatcher }
}

func NewService(cfg config.Config, store db.Store, objects blob.Store, logger *slog.Logger, options ...Option) *Service {
	service := &Service{
		cfg:     cfg,
		store:   store,
		objects: objects,
		logger:  logger,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) CreateDraft(ctx context.Context, logicPath string) (db.ShareRecord, error) {
	if strings.TrimSpace(s.cfg.ShareGCSBucket) == "" {
		return db.ShareRecord{}, errors.New("SHARE_GCS_BUCKET is required")
	}

	file, found, err := s.store.Find(ctx, cleanPath(logicPath))
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
	record, found, err := s.store.FindShare(ctx, id)
	if err != nil {
		return db.ShareRecord{}, err
	}
	if !found {
		return db.ShareRecord{}, db.ErrNotFound
	}
	if record.Status == StatusUploading {
		return record, nil
	}
	if record.Status != StatusDraft && record.Status != StatusFailed &&
		record.Status != StatusNotifyFailed && record.Status != StatusEmailFailedLegacy {
		return record, nil
	}

	record, err = s.store.MarkShareUploading(ctx, id, strings.TrimSpace(s.cfg.TelegramChatID))
	if err != nil {
		return db.ShareRecord{}, err
	}
	if s.dispatcher == nil {
		go func() {
			if err := s.ProcessShareJob(context.Background(), Job{Version: JobVersion, ShareID: id}); err != nil {
				s.logger.Error("process share job", "share_id", id, "error", err)
			}
		}()
	} else if err := s.dispatcher.Dispatch(ctx, Job{Version: JobVersion, ShareID: id}); err != nil {
		_, _ = s.store.MarkShareFailed(context.Background(), id, StatusFailed, err.Error())
		return db.ShareRecord{}, fmt.Errorf("dispatch share job: %w", err)
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
	record, claimed, err := s.store.ClaimShareJob(ctx, job.ShareID, owner, time.Now().Add(shareLeaseDuration))
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
		return fmt.Errorf("share job is already leased: %s", job.ShareID)
	}
	defer func() {
		if err := s.store.ReleaseShareJob(context.Background(), job.ShareID, owner); err != nil {
			s.logger.Error("release share job lease", "share_id", job.ShareID, "error", err)
		}
	}()

	if record.Status == StatusNotified {
		return nil
	}
	if record.CompletedAt == nil {
		if err := s.upload(ctx, record); err != nil {
			s.logger.Error("share upload failed", "share_id", job.ShareID, "error", err)
			_, _ = s.store.MarkShareFailed(context.Background(), job.ShareID, StatusFailed, err.Error())
			return err
		}

		record, err = s.store.MarkShareUploaded(context.Background(), job.ShareID)
		if err != nil {
			return fmt.Errorf("mark share uploaded: %w", err)
		}
	}

	if strings.TrimSpace(s.cfg.TelegramBotToken) == "" || strings.TrimSpace(s.cfg.TelegramChatID) == "" {
		err := errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
		_, _ = s.store.MarkShareFailed(context.Background(), job.ShareID, StatusNotifyFailed, err.Error())
		return Permanent(err)
	}
	if err := s.sendNotification(ctx, record); err != nil {
		s.logger.Error("share telegram notification failed", "share_id", job.ShareID, "chat_id", s.cfg.TelegramChatID, "error", err)
		_, _ = s.store.MarkShareFailed(context.Background(), job.ShareID, StatusNotifyFailed, err.Error())
		return err
	}
	if _, err := s.store.MarkShareNotified(context.Background(), job.ShareID); err != nil {
		return fmt.Errorf("mark share notified: %w", err)
	}
	return nil
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

func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value)
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
