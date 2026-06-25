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
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/db"
)

const (
	StatusDraft             = "draft"
	StatusUploading         = "uploading"
	StatusCompleted         = "completed"
	StatusNotified          = "notified"
	StatusFailed            = "failed"
	StatusNotifyFailed      = "notification_failed"
	StatusEmailFailedLegacy = "email_failed"
)

type Service struct {
	cfg     config.Config
	store   *db.Store
	objects blob.Store
	logger  *slog.Logger
}

func NewService(cfg config.Config, store *db.Store, objects blob.Store, logger *slog.Logger) *Service {
	return &Service{
		cfg:     cfg,
		store:   store,
		objects: objects,
		logger:  logger,
	}
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
	go s.run(id)
	return record, nil
}

func (s *Service) run(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	record, found, err := s.store.FindShare(ctx, id)
	if err != nil || !found {
		s.logger.Error("load share for upload", "share_id", id, "error", err)
		return
	}

	if err := s.upload(ctx, record); err != nil {
		s.logger.Error("share upload failed", "share_id", id, "error", err)
		_, _ = s.store.MarkShareFailed(context.Background(), id, StatusFailed, err.Error())
		return
	}

	record, err = s.store.MarkShareUploaded(context.Background(), id)
	if err != nil {
		s.logger.Error("mark share uploaded", "share_id", id, "error", err)
		return
	}

	if strings.TrimSpace(s.cfg.TelegramBotToken) == "" || strings.TrimSpace(s.cfg.TelegramChatID) == "" {
		_, _ = s.store.MarkShareFailed(context.Background(), id, StatusNotifyFailed, "TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
		return
	}
	if err := s.sendNotification(ctx, record); err != nil {
		s.logger.Error("share telegram notification failed", "share_id", id, "chat_id", s.cfg.TelegramChatID, "error", err)
		_, _ = s.store.MarkShareFailed(context.Background(), id, StatusNotifyFailed, err.Error())
		return
	}
	if _, err := s.store.MarkShareNotified(context.Background(), id); err != nil {
		s.logger.Error("mark share notified", "share_id", id, "error", err)
	}
}

func (s *Service) upload(ctx context.Context, record db.ShareRecord) error {
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
	writer.Metadata = map[string]string{
		"vfs-link-logic-path": record.LogicPath,
		"vfs-link-share-id":   record.ID,
	}
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
		return fmt.Errorf(
			"telegram sendMessage status %d: %s",
			response.StatusCode,
			redactTelegramToken(strings.TrimSpace(string(body)), botToken),
		)
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
