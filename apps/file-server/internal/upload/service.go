package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusPending   = "pending"
	StatusUploading = "uploading"
	StatusUploaded  = "uploaded"
	StatusComplete  = "complete"
	StatusFailed    = "failed"
	defaultTTL      = 24 * time.Hour
	DefaultMaxBytes = int64(50) * 1024 * 1024 * 1024
)

var (
	ErrNotFound       = errors.New("upload session not found")
	ErrFileExists     = errors.New("file already exists")
	ErrConflict       = errors.New("file changed while upload was in progress")
	ErrInvalidSession = errors.New("upload session is not ready")
)

type Session struct {
	ID                   string            `json:"id"`
	LogicPath            string            `json:"logicPath"`
	PhysicalHash         string            `json:"physicalHash"`
	Driver               string            `json:"driver"`
	Size                 int64             `json:"size"`
	UploadedSize         int64             `json:"uploadedSize,omitempty"`
	ContentType          string            `json:"contentType"`
	Status               string            `json:"status"`
	Error                string            `json:"error,omitempty"`
	Overwrite            bool              `json:"overwrite"`
	ExpectedPhysicalHash *string           `json:"expectedPhysicalHash,omitempty"`
	RequireAbsent        bool              `json:"requireAbsent"`
	UploadURL            string            `json:"-"`
	UploadHeaders        map[string]string `json:"-"`
	CreatedAt            time.Time         `json:"createdAt"`
	UpdatedAt            time.Time         `json:"updatedAt"`
	ExpiresAt            time.Time         `json:"expiresAt"`
}

type CreateInput struct {
	LogicPath   string
	Size        int64
	ContentType string
	Overwrite   bool
}

type Repository interface {
	CreateUpload(context.Context, Session) error
	FindUpload(context.Context, string) (Session, bool, error)
	UpdateUpload(context.Context, Session) error
	DeleteUpload(context.Context, string) error
}

type File struct {
	PhysicalHash string
	IsDirectory  bool
}

type Publisher interface {
	FindFile(context.Context, string) (File, bool, error)
	ReplaceFile(context.Context, string, string, int64, *string, bool) (previous string, matched bool, err error)
}

type PreparedTarget struct {
	URL     string
	Headers map[string]string
}

// Storage keeps the HTTP contract independent of the concrete local/GCS
// implementation. For local storage Prepare returns an empty URL and the API
// exposes its own PUT endpoint. GCS returns a resumable session URL.
type Storage interface {
	Driver() string
	Prepare(context.Context, Session) (PreparedTarget, error)
	Write(context.Context, string, io.Reader) (int64, error)
	Stat(context.Context, string) (int64, error)
	Delete(context.Context, string) error
}

type Service struct {
	repository Repository
	files      Publisher
	storage    Storage
	now        func() time.Time
	ttl        time.Duration
	maxBytes   int64
}

type Option func(*Service)

func WithTTL(ttl time.Duration) Option {
	return func(service *Service) {
		if ttl > 0 {
			service.ttl = ttl
		}
	}
}

func WithMaxBytes(maxBytes int64) Option {
	return func(service *Service) {
		if maxBytes > 0 {
			service.maxBytes = maxBytes
		}
	}
}

func New(repository Repository, files Publisher, storage Storage, options ...Option) *Service {
	service := &Service{repository: repository, files: files, storage: storage, now: time.Now, ttl: defaultTTL, maxBytes: DefaultMaxBytes}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Session, error) {
	logicPath := cleanPath(input.LogicPath)
	if logicPath == "/" || path.Base(logicPath) == "." {
		return Session{}, errors.New("a file path is required")
	}
	if input.Size < 0 {
		return Session{}, errors.New("size must be non-negative")
	}
	if input.Size > s.maxBytes {
		return Session{}, fmt.Errorf("file size exceeds maximum of %d bytes", s.maxBytes)
	}
	existing, found, err := s.files.FindFile(ctx, logicPath)
	if err != nil {
		return Session{}, err
	}
	if found && existing.IsDirectory {
		return Session{}, errors.New("upload destination is a directory")
	}
	if found && !input.Overwrite {
		return Session{}, ErrFileExists
	}

	now := s.now().UTC()
	session := Session{
		ID:            uuid.NewString(),
		LogicPath:     logicPath,
		PhysicalHash:  uuid.NewString() + path.Ext(logicPath),
		Driver:        s.storage.Driver(),
		Size:          input.Size,
		ContentType:   strings.TrimSpace(input.ContentType),
		Status:        StatusPending,
		Overwrite:     input.Overwrite,
		RequireAbsent: !found,
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(s.ttl),
	}
	if found {
		expected := existing.PhysicalHash
		session.ExpectedPhysicalHash = &expected
	}
	prepared, err := s.storage.Prepare(ctx, session)
	if err != nil {
		return Session{}, fmt.Errorf("prepare upload: %w", err)
	}
	session.UploadURL = prepared.URL
	session.UploadHeaders = prepared.Headers
	if err := s.repository.CreateUpload(ctx, session); err != nil {
		_ = s.storage.Delete(ctx, session.PhysicalHash)
		return Session{}, err
	}
	return session, nil
}

func (s *Service) Find(ctx context.Context, id string) (Session, error) {
	session, found, err := s.repository.FindUpload(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if !found {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (s *Service) Write(ctx context.Context, id string, body io.Reader) (Session, error) {
	session, err := s.Find(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.Status != StatusPending && session.Status != StatusFailed {
		return Session{}, ErrInvalidSession
	}
	if s.now().After(session.ExpiresAt) {
		return Session{}, errors.New("upload session expired")
	}
	session.Status = StatusUploading
	session.Error = ""
	session.UpdatedAt = s.now().UTC()
	if err := s.repository.UpdateUpload(ctx, session); err != nil {
		return Session{}, err
	}
	// Read at most one byte beyond the declared size. This detects oversized
	// local uploads without allowing an unbounded request body to fill storage.
	written, writeErr := s.storage.Write(ctx, session.PhysicalHash, io.LimitReader(body, session.Size+1))
	session.UploadedSize = written
	if writeErr != nil {
		session.Status = StatusFailed
		session.Error = writeErr.Error()
	} else if written != session.Size {
		writeErr = fmt.Errorf("uploaded size %d does not match declared size %d", written, session.Size)
		session.Status = StatusFailed
		session.Error = writeErr.Error()
		_ = s.storage.Delete(ctx, session.PhysicalHash)
	} else {
		session.Status = StatusUploaded
	}
	session.UpdatedAt = s.now().UTC()
	if err := s.repository.UpdateUpload(ctx, session); err != nil && writeErr == nil {
		return Session{}, err
	}
	return session, writeErr
}

func (s *Service) Complete(ctx context.Context, id string) (Session, error) {
	session, err := s.Find(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.Status == StatusComplete {
		return session, nil
	}
	if session.Status != StatusPending && session.Status != StatusUploaded {
		return Session{}, ErrInvalidSession
	}
	size, err := s.storage.Stat(ctx, session.PhysicalHash)
	if err != nil {
		return Session{}, fmt.Errorf("verify upload: %w", err)
	}
	if size != session.Size {
		return Session{}, fmt.Errorf("uploaded size %d does not match declared size %d", size, session.Size)
	}
	previous, matched, err := s.files.ReplaceFile(ctx, session.LogicPath, session.PhysicalHash, size, session.ExpectedPhysicalHash, session.RequireAbsent)
	if err != nil {
		return Session{}, err
	}
	if !matched {
		return Session{}, ErrConflict
	}
	session.Status = StatusComplete
	session.UploadedSize = size
	session.Error = ""
	session.UpdatedAt = s.now().UTC()
	if err := s.repository.UpdateUpload(ctx, session); err != nil {
		return Session{}, err
	}
	if previous != "" && previous != session.PhysicalHash {
		_ = s.storage.Delete(ctx, previous)
	}
	return session, nil
}

func (s *Service) Cancel(ctx context.Context, id string) error {
	session, err := s.Find(ctx, id)
	if err != nil {
		return err
	}
	if session.Status != StatusComplete {
		if err := s.storage.Delete(ctx, session.PhysicalHash); err != nil {
			return err
		}
	}
	return s.repository.DeleteUpload(ctx, id)
}

func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value)
}
