package share

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"net/smtp"
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
	StatusDraft       = "draft"
	StatusUploading   = "uploading"
	StatusCompleted   = "completed"
	StatusEmailSent   = "email_sent"
	StatusFailed      = "failed"
	StatusEmailFailed = "email_failed"
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

func (s *Service) Start(ctx context.Context, id, email string) (db.ShareRecord, error) {
	email = strings.TrimSpace(email)
	if email != "" {
		parsedEmail, err := mail.ParseAddress(email)
		if err != nil {
			return db.ShareRecord{}, fmt.Errorf("invalid email address")
		}
		email = parsedEmail.Address
	}

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
	if record.Status != StatusDraft && record.Status != StatusFailed && record.Status != StatusEmailFailed {
		return record, nil
	}

	record, err = s.store.MarkShareUploading(ctx, id, email)
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

	if strings.TrimSpace(record.Email) == "" {
		return
	}
	if err := s.sendNotification(record); err != nil {
		s.logger.Error("share email failed", "share_id", id, "email", record.Email, "error", err)
		_, _ = s.store.MarkShareFailed(context.Background(), id, StatusEmailFailed, err.Error())
		return
	}
	if _, err := s.store.MarkShareEmailSent(context.Background(), id); err != nil {
		s.logger.Error("mark share email sent", "share_id", id, "error", err)
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

func (s *Service) sendNotification(record db.ShareRecord) error {
	if strings.TrimSpace(record.Email) == "" {
		return nil
	}
	if strings.TrimSpace(s.cfg.SMTPHost) == "" || strings.TrimSpace(s.cfg.SMTPFrom) == "" {
		return errors.New("SMTP_HOST and SMTP_FROM are required to send share email")
	}
	from, err := mail.ParseAddress(s.cfg.SMTPFrom)
	if err != nil {
		return fmt.Errorf("invalid SMTP_FROM address")
	}
	to, err := mail.ParseAddress(record.Email)
	if err != nil {
		return fmt.Errorf("invalid share email address")
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)
	var auth smtp.Auth
	if strings.TrimSpace(s.cfg.SMTPUser) != "" || strings.TrimSpace(s.cfg.SMTPPass) != "" {
		auth = smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPass, s.cfg.SMTPHost)
	}

	subject := fmt.Sprintf("vfs-link shared file: %s", sanitizeHeader(record.FileName))
	body := fmt.Sprintf("File: %s\nLink: %s\n", record.FileName, record.ShareURL)
	message := strings.Join([]string{
		fmt.Sprintf("From: %s", from.String()),
		fmt.Sprintf("To: %s", to.String()),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	return smtp.SendMail(addr, auth, from.Address, []string{to.Address}, []byte(message))
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

func sanitizeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
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
