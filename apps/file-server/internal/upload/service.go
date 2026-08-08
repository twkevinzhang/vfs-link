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
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

const (
	StatusPending   = "pending"
	StatusUploading = "uploading"
	StatusUploaded  = "uploaded"
	StatusComplete  = "complete"
	StatusFailed    = "failed"
	StatusExpired   = "expired"
	defaultTTL      = 24 * time.Hour
	DefaultMaxBytes = int64(50) * 1024 * 1024 * 1024
)

var (
	ErrNotFound                = errors.New("upload session not found")
	ErrFileExists              = errors.New("file already exists")
	ErrConflict                = errors.New("file changed while upload was in progress")
	ErrInvalidSession          = errors.New("upload session is not ready")
	ErrCancellationUnavailable = errors.New("upload session cannot be safely cancelled")
	ErrOffsetConflict          = errors.New("upload offset does not match committed size")
	ErrExpired                 = errors.New("upload session expired")
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
	UploadOrigin         string            `json:"-"`
	CreatedAt            time.Time         `json:"createdAt"`
	UpdatedAt            time.Time         `json:"updatedAt"`
	ExpiresAt            time.Time         `json:"expiresAt"`
}

type CreateInput struct {
	LogicPath   string
	Size        int64
	ContentType string
	Overwrite   bool
	Origin      string
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
	EnsureDirectory(context.Context, string) error
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
	Write(context.Context, Session, io.Reader) (int64, error)
	Stat(context.Context, string) (int64, error)
	Delete(context.Context, string) error
	Cancel(context.Context, Session) error
}

type resumableStorage interface {
	Storage
	WriteChunk(context.Context, Session, int64, io.Reader) (int64, error)
	RollbackChunk(context.Context, Session, int64) error
	Offset(context.Context, Session) (uploadedSize int64, complete bool, err error)
	Finalize(context.Context, Session) (int64, error)
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
	logicPath, err := logicpath.Parse(input.LogicPath)
	if err != nil {
		return Session{}, err
	}
	if logicPath == "" || path.Base(logicPath) == "." {
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
	physicalHash, err := objectkey.FromLogicalPath(logicPath)
	if err != nil {
		return Session{}, fmt.Errorf("create object key: %w", err)
	}
	session := Session{
		ID:            uuid.NewString(),
		LogicPath:     logicPath,
		PhysicalHash:  physicalHash,
		Driver:        s.storage.Driver(),
		Size:          input.Size,
		ContentType:   strings.TrimSpace(input.ContentType),
		UploadOrigin:  strings.TrimSpace(input.Origin),
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
		_ = s.storage.Cancel(ctx, session)
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
	if session.Status != StatusComplete && session.Status != StatusExpired && s.now().After(session.ExpiresAt) {
		session.Status = StatusExpired
		session.Error = ErrExpired.Error()
		session.UpdatedAt = s.now().UTC()
		if err := s.repository.UpdateUpload(ctx, session); err != nil {
			return Session{}, err
		}
		return session, nil
	}
	if storage, ok := s.storage.(resumableStorage); ok && session.Status != StatusUploaded && session.Status != StatusComplete && session.Status != StatusExpired {
		uploadedSize, complete, err := storage.Offset(ctx, session)
		if err != nil {
			return Session{}, fmt.Errorf("query upload offset: %w", err)
		}
		if uploadedSize != session.UploadedSize || complete || (uploadedSize == session.Size && session.Status != StatusUploaded) {
			session.UploadedSize = uploadedSize
			if complete {
				session.Status = StatusUploaded
			} else if uploadedSize > 0 {
				session.Status = StatusUploading
			}
			session.UpdatedAt = s.now().UTC()
			if err := s.repository.UpdateUpload(ctx, session); err != nil {
				return Session{}, err
			}
		}
	}
	return session, nil
}

func (s *Service) Write(ctx context.Context, id string, body io.Reader) (Session, error) {
	session, err := s.Find(ctx, id)
	if err != nil {
		return Session{}, err
	}
	end := session.Size - 1
	return s.WriteChunk(ctx, id, 0, end, session.Size, body)
}

func (s *Service) WriteChunk(ctx context.Context, id string, start, end, total int64, body io.Reader) (Session, error) {
	session, err := s.Find(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.Status == StatusExpired {
		return session, ErrExpired
	}
	if session.Status != StatusPending && session.Status != StatusUploading && session.Status != StatusFailed {
		return Session{}, ErrInvalidSession
	}
	if total != session.Size || start < 0 || (total == 0 && (start != 0 || end != -1)) || (total > 0 && (end < start || end >= total)) {
		return session, errors.New("invalid Content-Range for upload session")
	}
	if start != session.UploadedSize {
		return session, fmt.Errorf("%w: got %d, want %d", ErrOffsetConflict, start, session.UploadedSize)
	}
	session.Status = StatusUploading
	session.Error = ""
	session.UpdatedAt = s.now().UTC()
	if err := s.repository.UpdateUpload(ctx, session); err != nil {
		return Session{}, err
	}
	storage, ok := s.storage.(resumableStorage)
	if !ok {
		return Session{}, errors.New("storage does not support resumable uploads")
	}
	expectedBytes := end - start + 1
	committed, writeErr := storage.WriteChunk(ctx, session, start, io.LimitReader(body, expectedBytes))
	if writeErr == nil && committed == end+1 {
		var extra [1]byte
		if count, readErr := body.Read(extra[:]); count > 0 {
			writeErr = errors.New("request body size does not match Content-Range")
			if rollbackErr := storage.RollbackChunk(ctx, session, start); rollbackErr != nil {
				writeErr = errors.Join(writeErr, fmt.Errorf("rollback oversized chunk: %w", rollbackErr))
			} else {
				committed = start
			}
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			writeErr = readErr
		}
	}
	session.UploadedSize = committed
	if writeErr != nil {
		session.Status = StatusUploading
		session.Error = writeErr.Error()
	} else if committed != end+1 {
		writeErr = fmt.Errorf("committed size %d does not match chunk end %d", committed, end)
		session.Status = StatusUploading
		session.Error = writeErr.Error()
	} else if committed == session.Size {
		session.Status = StatusUploaded
	} else {
		session.Status = StatusUploading
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
	if session.Status == StatusExpired {
		return session, ErrExpired
	}
	if session.Status != StatusUploaded || session.UploadedSize != session.Size {
		return Session{}, ErrInvalidSession
	}
	storage, ok := s.storage.(resumableStorage)
	if !ok {
		return Session{}, errors.New("storage does not support resumable uploads")
	}
	size, err := storage.Finalize(ctx, session)
	if err != nil {
		return Session{}, fmt.Errorf("verify upload: %w", err)
	}
	if size != session.Size {
		return Session{}, fmt.Errorf("uploaded size %d does not match declared size %d", size, session.Size)
	}
	if err := ensureParentDirectories(ctx, s.files, session.LogicPath); err != nil {
		return Session{}, fmt.Errorf("ensure upload directories: %w", err)
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

func ensureParentDirectories(ctx context.Context, files Publisher, logicPath string) error {
	parent := logicpath.Parent(logicPath)
	if parent == "" {
		return nil
	}
	parents := make([]string, 0, strings.Count(parent, "/"))
	for current := parent; current != ""; current = logicpath.Parent(current) {
		parents = append(parents, current)
	}
	for i := len(parents) - 1; i >= 0; i-- {
		if err := files.EnsureDirectory(ctx, parents[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Cancel(ctx context.Context, id string) error {
	session, err := s.Find(ctx, id)
	if err != nil {
		return err
	}
	if session.Status != StatusComplete {
		if err := s.storage.Cancel(ctx, session); err != nil {
			return err
		}
	}
	return s.repository.DeleteUpload(ctx, id)
}
