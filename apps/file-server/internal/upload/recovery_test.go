package upload

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

// boundedRecoveryRepository is separate so the method records its actual
// argument while still embedding the complete in-memory Repository contract.
type boundedRecoveryRepository struct {
	*singleSessionRepository
	due       []Session
	listLimit int
	listErr   error
}

func (r *boundedRecoveryRepository) ListDueRecoveries(_ context.Context, _ time.Time, limit int) ([]Session, error) {
	r.listLimit = limit
	return r.due, r.listErr
}

func TestRecoverDueResumesExpiredObjectReadyCheckpoint(t *testing.T) {
	now := time.Now().UTC()
	expiredLease := now.Add(-time.Second)
	physicalHash, err := objectkey.ForUpload("docs/recovered.txt", "recover-completion")
	if err != nil {
		t.Fatal(err)
	}
	repository := &singleSessionRepository{session: Session{
		ID: "recover-completion", LogicPath: "docs/recovered.txt", PhysicalHash: physicalHash,
		Size: 4, UploadedSize: 4, Status: StatusFinalizing, CompletionStatus: CompletionObjectReady,
		CompletionOwner: "dead-worker", CompletionLeaseUntil: &expiredLease, RequireAbsent: true,
		ExpiresAt: now.Add(time.Hour), Revision: 3,
	}}
	publisher := &mappingPublisher{}
	service := New(repository, publisher, &completeStorage{})
	service.now = func() time.Time { return now }

	result, err := service.RecoverDue(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Completed != 1 || result.Retryable != 0 || result.Failed != 0 {
		t.Fatalf("RecoverDue() = %#v", result)
	}
	if repository.session.Status != StatusComplete || repository.session.CompletionStatus != CompletionComplete {
		t.Fatalf("recovered session = %#v", repository.session)
	}
	if publisher.calls != 1 {
		t.Fatalf("publish calls = %d, want 1", publisher.calls)
	}
}

func TestRecoverDueRetriesPendingCleanup(t *testing.T) {
	now := time.Now().UTC()
	repository := &singleSessionRepository{session: Session{
		ID: "recover-cleanup", LogicPath: "docs/recovered.txt", PhysicalHash: "new-generation",
		PreviousPhysicalHash: "old-generation", Status: StatusComplete, CompletionStatus: CompletionComplete,
		CleanupStatus: CleanupPending, ExpiresAt: now.Add(time.Hour), Revision: 7,
	}}
	publisher := &mappingPublisher{cleanup: CleanupResult{Pending: false}}
	service := New(repository, publisher, &completeStorage{})
	service.now = func() time.Time { return now }

	result, err := service.RecoverDue(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Cleaned != 1 || publisher.cleanupCalls != 1 {
		t.Fatalf("RecoverDue() = %#v, cleanup calls = %d", result, publisher.cleanupCalls)
	}
	if repository.session.CleanupStatus != CleanupComplete {
		t.Fatalf("cleanup status = %q", repository.session.CleanupStatus)
	}
}

func TestRecoverDueBoundsBatchAndClassifiesActiveRace(t *testing.T) {
	now := time.Now().UTC()
	activeLease := now.Add(time.Minute)
	session := Session{
		ID: "recover-raced", Status: StatusFinalizing, CompletionStatus: CompletionPending,
		CompletionOwner: "active-worker", CompletionLeaseUntil: &activeLease,
		Size: 4, UploadedSize: 4, ExpiresAt: now.Add(time.Hour),
	}
	repository := &boundedRecoveryRepository{
		singleSessionRepository: &singleSessionRepository{session: session},
		due:                     []Session{session},
	}
	service := New(repository, &mappingPublisher{}, &completeStorage{})
	service.now = func() time.Time { return now }

	result, err := service.RecoverDue(context.Background(), maxRecoveryBatch+100)
	if err != nil {
		t.Fatal(err)
	}
	if repository.listLimit != maxRecoveryBatch {
		t.Fatalf("list limit = %d, want %d", repository.listLimit, maxRecoveryBatch)
	}
	if result.InProgress != 1 || result.Failed != 0 {
		t.Fatalf("RecoverDue() = %#v", result)
	}
}

func TestRunRecoveryStopsOnContextCancellationAndScanError(t *testing.T) {
	repository := &boundedRecoveryRepository{singleSessionRepository: &singleSessionRepository{}}
	service := New(repository, &mappingPublisher{}, &completeStorage{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.RunRecovery(ctx, time.Hour, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunRecovery(cancelled) error = %v", err)
	}

	repository.listErr = errors.New("metadata unavailable")
	if err := service.RunRecovery(context.Background(), time.Hour, 1); err == nil || !errors.Is(err, repository.listErr) {
		t.Fatalf("RunRecovery(scan error) = %v", err)
	}
}
