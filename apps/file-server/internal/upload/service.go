package upload

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	StatusPending          = "pending"
	StatusUploading        = "uploading"
	StatusUploaded         = "uploaded"
	StatusFinalizing       = "finalizing"
	StatusComplete         = "complete"
	StatusConflict         = "conflict"
	StatusCancelling       = "cancelling"
	StatusCancelled        = "cancelled"
	StatusFailed           = "failed"
	StatusExpired          = "expired"
	defaultTTL             = 24 * time.Hour
	defaultCompletionLease = 30 * time.Second
	defaultRecoveryBatch   = 50
	maxRecoveryBatch       = 500
	DefaultMaxBytes        = int64(50) * 1024 * 1024 * 1024
)

const (
	CleanupPending  = "pending"
	CleanupComplete = "complete"
)

const (
	CompletionPending     = "pending"
	CompletionObjectReady = "object_ready"
	CompletionPublished   = "published"
	CompletionComplete    = "complete"
	CompletionConflict    = "conflict"
)

var (
	ErrNotFound                = errors.New("upload session not found")
	ErrFileExists              = errors.New("file already exists")
	ErrTargetIsDirectory       = errors.New("upload destination is a directory")
	ErrConflict                = errors.New("file changed while upload was in progress")
	ErrTargetChanged           = errors.New("upload target changed after preflight")
	ErrTargetVersionRequired   = errors.New("targetVersion is required when overwrite is true")
	ErrInvalidSession          = errors.New("upload session is not ready")
	ErrCancellationUnavailable = errors.New("upload session cannot be safely cancelled")
	ErrOffsetConflict          = errors.New("upload offset does not match committed size")
	ErrExpired                 = errors.New("upload session expired")
	ErrCompletionInProgress    = errors.New("upload completion is already in progress")
	ErrCompletionRetryable     = errors.New("upload completion can be retried")
	ErrCleanupRetryable        = errors.New("upload cleanup can be retried")
	ErrCleanupUnavailable      = errors.New("upload cleanup retrier is unavailable")
	ErrLegacyObjectKey         = errors.New("upload session uses a legacy mutable object key; create a new upload session")
	ErrResumableSessionGone    = errors.New("resumable upload session is no longer available")
)

type Session struct {
	ID                      string            `json:"id"`
	LogicPath               string            `json:"logicPath"`
	PhysicalHash            string            `json:"physicalHash"`
	Driver                  string            `json:"driver"`
	Size                    int64             `json:"size"`
	UploadedSize            int64             `json:"uploadedSize,omitempty"`
	ContentType             string            `json:"contentType"`
	Status                  string            `json:"status"`
	Error                   string            `json:"error,omitempty"`
	Overwrite               bool              `json:"overwrite"`
	ExpectedPhysicalHash    *string           `json:"expectedPhysicalHash,omitempty"`
	ExpectedFileID          int               `json:"expectedFileId,omitempty"`
	ExpectedFileUpdatedAt   *time.Time        `json:"expectedFileUpdatedAt,omitempty"`
	RequireAbsent           bool              `json:"requireAbsent"`
	UploadURL               string            `json:"-"`
	UploadHeaders           map[string]string `json:"-"`
	UploadOrigin            string            `json:"-"`
	CreatedAt               time.Time         `json:"createdAt"`
	UpdatedAt               time.Time         `json:"updatedAt"`
	ExpiresAt               time.Time         `json:"expiresAt"`
	Revision                int64             `json:"revision"`
	CompletionStatus        string            `json:"completionStatus,omitempty"`
	CompletionOwner         string            `json:"-"`
	CompletionLeaseUntil    *time.Time        `json:"-"`
	CompletionAttempts      int               `json:"completionAttempts,omitempty"`
	CompletionNextAttemptAt *time.Time        `json:"completionNextAttemptAt,omitempty"`
	FinalizedAt             *time.Time        `json:"finalizedAt,omitempty"`
	PublishedAt             *time.Time        `json:"publishedAt,omitempty"`
	CompletedAt             *time.Time        `json:"completedAt,omitempty"`
	LastCompletionError     string            `json:"lastCompletionError,omitempty"`
	CancelRequestedAt       *time.Time        `json:"cancelRequestedAt,omitempty"`
	CancelledAt             *time.Time        `json:"cancelledAt,omitempty"`
	PreviousPhysicalHash    string            `json:"previousPhysicalHash,omitempty"`
	CleanupStatus           string            `json:"cleanupStatus,omitempty"`
	CleanupError            string            `json:"cleanupError,omitempty"`
}

type CreateInput struct {
	LogicPath     string
	Size          int64
	ContentType   string
	Overwrite     bool
	TargetVersion string
	Origin        string
}

type Repository interface {
	CreateUpload(context.Context, Session) error
	FindUpload(context.Context, string) (Session, bool, error)
	ListDueRecoveries(context.Context, time.Time, int) ([]Session, error)
	UpdateUpload(context.Context, Session, int64) (Session, bool, error)
	RequestCompletion(context.Context, string, time.Time) (Session, bool, error)
	ClaimCompletion(context.Context, string, string, time.Time, time.Time) (Session, bool, error)
	MarkObjectReady(context.Context, string, string, time.Time) (Session, error)
	MarkPublished(context.Context, string, string, string, string, string, time.Time) (Session, error)
	MarkComplete(context.Context, string, string, time.Time) (Session, error)
	RetryCompletion(context.Context, string, string, string, time.Time, time.Time) (Session, error)
	MarkCompletionConflict(context.Context, string, string, string, time.Time) (Session, error)
	RequestCancel(context.Context, string, time.Time) (Session, bool, error)
	MarkCancelled(context.Context, string, time.Time) (Session, error)
	ExpireUpload(context.Context, string, int64, time.Time) (Session, bool, error)
	MarkCleanupComplete(context.Context, string, time.Time) (Session, error)
	RetryCleanup(context.Context, string, string, time.Time) (Session, error)
}

type File struct {
	ID           int
	PhysicalHash string
	IsDirectory  bool
	Size         int64
	UpdatedAt    time.Time
}

type FileSnapshot struct {
	ID           int
	UpdatedAt    time.Time
	PhysicalHash string
}

const (
	PreflightAvailable = "available"
	PreflightConflict  = "conflict"
	PreflightDirectory = "directory"
)

type PreflightInput struct {
	ClientID  string
	LogicPath string
}

type ExistingMetadata struct {
	Kind      string    `json:"kind"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type PreflightResult struct {
	ClientID      string            `json:"clientId"`
	LogicPath     string            `json:"path"`
	Status        string            `json:"status"`
	Existing      *ExistingMetadata `json:"existing,omitempty"`
	TargetVersion string            `json:"targetVersion"`
}

type Publisher interface {
	FindFile(context.Context, string) (File, bool, error)
	EnsureDirectory(context.Context, string) error
	ReplaceFile(context.Context, string, string, int64, *string, *FileSnapshot, bool) (PublishResult, bool, error)
}

// UploadGenerationReferenceChecker is an optional safety boundary used when a
// completion loses the namespace CAS. A former winner may still be referenced
// by Share or Trash even when the active mapping has moved again.
type UploadGenerationReferenceChecker interface {
	IsUploadGenerationReferenced(context.Context, string) (bool, error)
}

type PublishResult struct {
	PreviousPhysicalHash string
	CleanupPending       bool
	CleanupError         string
}

type CleanupIntent struct {
	UploadID              string
	LogicPath             string
	PublishedPhysicalHash string
	PreviousPhysicalHash  string
}

type CleanupResult struct {
	Pending bool
	Error   string
}

type CleanupRetrier interface {
	RetryUploadCleanup(context.Context, CleanupIntent) (CleanupResult, error)
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
	repository      Repository
	files           Publisher
	storage         Storage
	now             func() time.Time
	ttl             time.Duration
	maxBytes        int64
	completionLease time.Duration
}

// RecoveryResult reports one bounded durable-recovery scan. Item failures are
// classified instead of aborting the batch, so one poisoned upload cannot
// prevent other due sessions from advancing.
type RecoveryResult struct {
	Scanned    int
	Completed  int
	Cleaned    int
	InProgress int
	Retryable  int
	Terminal   int
	Failed     int
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
	service := &Service{repository: repository, files: files, storage: storage, now: time.Now, ttl: defaultTTL, maxBytes: DefaultMaxBytes, completionLease: defaultCompletionLease}
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
	if input.Overwrite && strings.TrimSpace(input.TargetVersion) == "" {
		return Session{}, ErrTargetVersionRequired
	}
	if targetVersion := strings.TrimSpace(input.TargetVersion); targetVersion != "" && targetVersion != targetVersionFor(logicPath, existing, found) {
		return Session{}, ErrTargetChanged
	}
	if found && existing.IsDirectory {
		return Session{}, ErrTargetIsDirectory
	}
	if found && !input.Overwrite {
		return Session{}, ErrFileExists
	}

	now := s.now().UTC()
	uploadID := uuid.NewString()
	physicalHash, err := objectkey.ForUpload(logicPath, uploadID)
	if err != nil {
		return Session{}, fmt.Errorf("create object key: %w", err)
	}
	session := Session{
		ID:            uploadID,
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
		session.ExpectedFileID = existing.ID
		expectedUpdatedAt := existing.UpdatedAt
		session.ExpectedFileUpdatedAt = &expectedUpdatedAt
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

// Preflight snapshots the current logical-path targets before the browser
// starts any upload. The opaque target version is bound to both the normalized
// path and its current metadata identity so Create can reject stale overwrite
// decisions before allocating an upload session or GCS resumable capability.
func (s *Service) Preflight(ctx context.Context, inputs []PreflightInput) ([]PreflightResult, error) {
	results := make([]PreflightResult, 0, len(inputs))
	for _, input := range inputs {
		clientID := strings.TrimSpace(input.ClientID)
		if clientID == "" {
			return nil, errors.New("clientId is required")
		}
		logicPath, err := logicpath.Parse(input.LogicPath)
		if err != nil {
			return nil, err
		}
		if logicPath == "" || path.Base(logicPath) == "." {
			return nil, errors.New("a file path is required")
		}
		existing, found, err := s.files.FindFile(ctx, logicPath)
		if err != nil {
			return nil, err
		}
		result := PreflightResult{
			ClientID: clientID, LogicPath: logicPath, Status: PreflightAvailable,
			TargetVersion: targetVersionFor(logicPath, existing, found),
		}
		if found {
			kind := "file"
			result.Status = PreflightConflict
			if existing.IsDirectory {
				kind = "directory"
				result.Status = PreflightDirectory
			}
			result.Existing = &ExistingMetadata{Kind: kind, Size: existing.Size, UpdatedAt: existing.UpdatedAt}
		}
		results = append(results, result)
	}
	return results, nil
}

func targetVersionFor(logicPath string, file File, found bool) string {
	state := "absent"
	if found {
		state = fmt.Sprintf("present\x00%d\x00%t\x00%s\x00%d\x00%s", file.ID, file.IsDirectory, file.PhysicalHash, file.Size, file.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	digest := sha256.Sum256([]byte(logicPath + "\x00" + state))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Service) Find(ctx context.Context, id string) (Session, error) {
	session, found, err := s.repository.FindUpload(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if !found {
		return Session{}, ErrNotFound
	}
	if session.Status == StatusExpired && session.Error == "" {
		session.Error = ErrExpired.Error()
	}
	now := s.now().UTC()
	remoteCompleted := false
	if storage, ok := s.storage.(resumableStorage); ok && uploadCanReconcileOffset(session.Status) {
		uploadedSize, complete, err := storage.Offset(ctx, session)
		if err != nil {
			if !(errors.Is(err, ErrResumableSessionGone) && uploadCanExpire(session.Status) && now.After(session.ExpiresAt)) {
				return Session{}, fmt.Errorf("query upload offset: %w", err)
			}
			expired, updated, expireErr := s.repository.ExpireUpload(ctx, session.ID, session.Revision, now)
			if expireErr != nil {
				return Session{}, expireErr
			}
			if updated {
				return expired, nil
			}
			return s.findUpload(ctx, id)
		}
		if uploadedSize != session.UploadedSize || complete || (uploadedSize == session.Size && session.Status != StatusUploaded) {
			session.UploadedSize = uploadedSize
			if complete {
				session.Status = StatusUploaded
			} else if uploadedSize > 0 {
				session.Status = StatusUploading
			}
			expectedRevision := session.Revision
			session.UpdatedAt = now
			updated, matched, updateErr := s.repository.UpdateUpload(ctx, session, expectedRevision)
			if updateErr != nil {
				return Session{}, updateErr
			}
			if !matched {
				return s.findUpload(ctx, id)
			}
			session = updated
			remoteCompleted = complete
		}
	}
	// A remote resumable upload may have committed before ExpiresAt while this
	// process was down. Reconcile that durable fact before applying expiry so a
	// completed object does not become permanently unpublishable on recovery.
	if !remoteCompleted && uploadCanExpire(session.Status) && now.After(session.ExpiresAt) {
		expired, updated, expireErr := s.repository.ExpireUpload(ctx, session.ID, session.Revision, now)
		if expireErr != nil {
			return Session{}, expireErr
		}
		if updated {
			if expired.Error == "" {
				expired.Error = ErrExpired.Error()
			}
			return expired, nil
		}
		return s.findUpload(ctx, id)
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
	expectedRevision := session.Revision
	session.Status = StatusUploading
	session.Error = ""
	session.UpdatedAt = s.now().UTC()
	updated, matched, err := s.repository.UpdateUpload(ctx, session, expectedRevision)
	if err != nil {
		return Session{}, err
	}
	if !matched {
		return Session{}, ErrInvalidSession
	}
	session = updated
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
	updated, matched, updateErr := s.repository.UpdateUpload(ctx, session, session.Revision)
	if updateErr != nil && writeErr == nil {
		return Session{}, updateErr
	}
	if !matched && writeErr == nil {
		return Session{}, ErrInvalidSession
	}
	if matched {
		session = updated
	}
	return session, writeErr
}

func (s *Service) Complete(ctx context.Context, id string) (Session, error) {
	session, err := s.Find(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.Status == StatusComplete {
		return s.completeWithBestEffortCleanup(ctx, session), nil
	}
	if session.Status == StatusConflict || session.CompletionStatus == CompletionConflict {
		return session, ErrConflict
	}
	if session.Status == StatusCancelled || session.Status == StatusCancelling {
		return session, ErrInvalidSession
	}
	if session.Status == StatusExpired {
		return session, ErrExpired
	}
	if session.Status != StatusUploaded && session.Status != StatusFinalizing {
		return Session{}, ErrInvalidSession
	}
	if session.UploadedSize != session.Size {
		return Session{}, ErrInvalidSession
	}
	now := s.now().UTC()
	session, _, err = s.repository.RequestCompletion(ctx, id, now)
	if err != nil {
		return Session{}, err
	}
	if terminal, terminalErr := completionTerminal(session); terminal {
		return session, terminalErr
	}
	owner := uuid.NewString()
	session, claimed, err := s.repository.ClaimCompletion(ctx, id, owner, now, now.Add(s.completionLease))
	if err != nil {
		return Session{}, err
	}
	if !claimed {
		if session.ID == "" {
			session, err = s.findUpload(ctx, id)
			if err != nil {
				return Session{}, err
			}
		}
		if terminal, terminalErr := completionTerminal(session); terminal {
			return session, terminalErr
		}
		return session, ErrCompletionInProgress
	}
	completed, err := s.runCompletion(ctx, session, owner)
	if err != nil {
		return completed, err
	}
	return s.completeWithBestEffortCleanup(ctx, completed), nil
}

func (s *Service) runCompletion(ctx context.Context, session Session, owner string) (Session, error) {
	if session.CompletionStatus != CompletionPublished &&
		!objectkey.IsUploadGeneration(session.LogicPath, session.ID, session.PhysicalHash) {
		conflicted, err := s.repository.MarkCompletionConflict(
			ctx, session.ID, owner, ErrLegacyObjectKey.Error(), s.now().UTC(),
		)
		if err != nil {
			return Session{}, errors.Join(ErrLegacyObjectKey, err)
		}
		return conflicted, ErrLegacyObjectKey
	}
	storage, ok := s.storage.(resumableStorage)
	if !ok {
		return s.retryCompletion(ctx, session, owner, errors.New("storage does not support resumable uploads"))
	}
	if session.CompletionStatus == "" || session.CompletionStatus == CompletionPending {
		size, err := storage.Finalize(ctx, session)
		if err != nil {
			return s.retryCompletion(ctx, session, owner, fmt.Errorf("verify upload: %w", err))
		}
		if size != session.Size {
			return s.retryCompletion(ctx, session, owner, fmt.Errorf("uploaded size %d does not match declared size %d", size, session.Size))
		}
		session, err = s.repository.MarkObjectReady(ctx, session.ID, owner, s.now().UTC())
		if err != nil {
			return Session{}, errors.Join(ErrCompletionRetryable, err)
		}
	}
	if session.CompletionStatus == CompletionObjectReady {
		if err := ensureParentDirectories(ctx, s.files, session.LogicPath); err != nil {
			return s.retryCompletion(ctx, session, owner, fmt.Errorf("ensure upload directories: %w", err))
		}
		var expectedSnapshot *FileSnapshot
		if session.ExpectedFileID > 0 && session.ExpectedFileUpdatedAt != nil {
			expectedSnapshot = &FileSnapshot{
				ID: session.ExpectedFileID, UpdatedAt: *session.ExpectedFileUpdatedAt,
				PhysicalHash: valueOrEmpty(session.ExpectedPhysicalHash),
			}
		}
		result, matched, err := s.files.ReplaceFile(
			ctx, session.LogicPath, session.PhysicalHash, session.Size,
			session.ExpectedPhysicalHash, expectedSnapshot, session.RequireAbsent,
		)
		if err != nil {
			return s.retryCompletion(ctx, session, owner, err)
		}
		if !matched {
			// This immutable generation never became visible. Remove it before
			// recording the terminal conflict so a losing upload cannot strand a
			// large object with no remaining recovery owner. Delete is idempotent;
			// a transient failure leaves the saga retryable instead of terminal.
			checker, ok := s.files.(UploadGenerationReferenceChecker)
			if !ok {
				return s.retryCompletion(ctx, session, owner, errors.New("upload generation reference checker is unavailable"))
			}
			referenced, referenceErr := checker.IsUploadGenerationReferenced(ctx, session.PhysicalHash)
			if referenceErr != nil {
				return s.retryCompletion(ctx, session, owner, fmt.Errorf("check conflicting upload generation references: %w", referenceErr))
			}
			if !referenced {
				if deleteErr := s.storage.Delete(ctx, session.PhysicalHash); deleteErr != nil {
					return s.retryCompletion(ctx, session, owner, fmt.Errorf("delete conflicting upload generation: %w", deleteErr))
				}
			}
			conflicted, markErr := s.repository.MarkCompletionConflict(ctx, session.ID, owner, ErrConflict.Error(), s.now().UTC())
			if markErr != nil {
				return Session{}, errors.Join(ErrConflict, markErr)
			}
			return conflicted, ErrConflict
		}
		cleanupStatus := CleanupComplete
		if result.CleanupPending {
			cleanupStatus = CleanupPending
		}
		session, err = s.repository.MarkPublished(
			ctx, session.ID, owner, result.PreviousPhysicalHash,
			cleanupStatus, result.CleanupError, s.now().UTC(),
		)
		if err != nil {
			return Session{}, errors.Join(ErrCompletionRetryable, err)
		}
	}
	if session.CompletionStatus == CompletionPublished {
		completed, err := s.repository.MarkComplete(ctx, session.ID, owner, s.now().UTC())
		if err != nil {
			return Session{}, errors.Join(ErrCompletionRetryable, err)
		}
		return completed, nil
	}
	if terminal, terminalErr := completionTerminal(session); terminal {
		return session, terminalErr
	}
	return s.retryCompletion(ctx, session, owner, fmt.Errorf("unknown completion checkpoint %q", session.CompletionStatus))
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Service) retryCompletion(ctx context.Context, session Session, owner string, cause error) (Session, error) {
	now := s.now().UTC()
	retried, err := s.repository.RetryCompletion(ctx, session.ID, owner, cause.Error(), now, now)
	if err != nil {
		return Session{}, errors.Join(ErrCompletionRetryable, cause, err)
	}
	return retried, errors.Join(ErrCompletionRetryable, cause)
}

func completionTerminal(session Session) (bool, error) {
	switch {
	case session.Status == StatusComplete || session.CompletionStatus == CompletionComplete:
		return true, nil
	case session.Status == StatusConflict || session.CompletionStatus == CompletionConflict:
		return true, ErrConflict
	case session.Status == StatusCancelled:
		return true, ErrInvalidSession
	default:
		return false, nil
	}
}

func uploadCanExpire(status string) bool {
	switch status {
	case StatusPending, StatusUploading, StatusFailed:
		return true
	default:
		return false
	}
}

func uploadCanReconcileOffset(status string) bool {
	switch status {
	case StatusPending, StatusUploading, StatusFailed:
		return true
	default:
		return false
	}
}

func (s *Service) findUpload(ctx context.Context, id string) (Session, error) {
	session, found, err := s.repository.FindUpload(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if !found {
		return Session{}, ErrNotFound
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
	if session.Status == StatusComplete || session.Status == StatusCancelled || session.Status == StatusConflict {
		return nil
	}
	if session.Driver == "gcs" && strings.TrimSpace(session.UploadURL) == "" {
		return ErrCancellationUnavailable
	}
	requested, needed, err := s.repository.RequestCancel(ctx, id, s.now().UTC())
	if err != nil {
		return err
	}
	if !needed {
		if requested.Status == StatusFinalizing {
			return ErrCancellationUnavailable
		}
		return nil
	}
	if err := s.storage.Cancel(ctx, requested); err != nil {
		return err
	}
	if requested.UploadedSize == requested.Size {
		// The immutable session object may already exist for a remotely
		// completed direct upload. It is still unpublished because completion
		// and cancellation are mutually exclusive, so deleting this exact key
		// is safe and keeps Cancel itself retryable on storage failure.
		if err := s.storage.Delete(ctx, requested.PhysicalHash); err != nil {
			return err
		}
	}
	_, err = s.repository.MarkCancelled(ctx, id, s.now().UTC())
	return err
}

func (s *Service) completeWithBestEffortCleanup(ctx context.Context, session Session) Session {
	if session.Status != StatusComplete || session.CleanupStatus != CleanupPending {
		return session
	}
	updated, _ := s.RetryCleanup(ctx, session.ID)
	if updated.ID != "" {
		return updated
	}
	return session
}

// RetryCleanup retries post-publication cleanup without changing the business
// completion result. Cleanup is deliberately a separate terminal concern: a
// previous-object or thumbnail deletion failure must never roll back the
// visible file mapping.
func (s *Service) RetryCleanup(ctx context.Context, id string) (Session, error) {
	session, err := s.findUpload(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.Status != StatusComplete {
		return session, ErrInvalidSession
	}
	if session.CleanupStatus != CleanupPending {
		return session, nil
	}
	retrier, ok := s.files.(CleanupRetrier)
	if !ok {
		return session, ErrCleanupUnavailable
	}
	result, retryErr := retrier.RetryUploadCleanup(ctx, CleanupIntent{
		UploadID: session.ID, LogicPath: session.LogicPath,
		PublishedPhysicalHash: session.PhysicalHash,
		PreviousPhysicalHash:  session.PreviousPhysicalHash,
	})
	if retryErr != nil || result.Pending {
		message := result.Error
		if retryErr != nil {
			message = retryErr.Error()
		}
		if strings.TrimSpace(message) == "" {
			message = ErrCleanupRetryable.Error()
		}
		updated, persistErr := s.repository.RetryCleanup(ctx, id, message, s.now().UTC())
		return updated, errors.Join(ErrCleanupRetryable, retryErr, persistErr)
	}
	return s.repository.MarkCleanupComplete(ctx, id, s.now().UTC())
}
