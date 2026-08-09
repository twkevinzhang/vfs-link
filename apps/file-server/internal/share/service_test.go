package share

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type fakeDispatcher struct {
	mu   sync.Mutex
	err  error
	jobs []Job
}

func (d *fakeDispatcher) Dispatch(_ context.Context, job Job) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.jobs = append(d.jobs, job)
	return d.err
}

type countedUploader struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (u *countedUploader) UploadShare(context.Context, db.ShareRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	return u.err
}

type countedNotifier struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (n *countedNotifier) NotifyShare(context.Context, db.ShareRecord) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	return n.err
}

type failUploadedOnceStore struct {
	db.Store
	mu        sync.Mutex
	remaining int
}

func (s *failUploadedOnceStore) MarkShareUploadedBy(ctx context.Context, id, owner string) (db.ShareRecord, error) {
	s.mu.Lock()
	if s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		return db.ShareRecord{}, errors.New("injected crash before completed state write")
	}
	s.mu.Unlock()
	return s.Store.MarkShareUploadedBy(ctx, id, owner)
}

func openShareTestStore(t *testing.T) db.Store {
	t.Helper()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "_share-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func createShareTestRecord(t *testing.T, store db.Store, id, status string, completed bool) db.ShareRecord {
	t.Helper()
	record := db.ShareRecord{
		ID: id, LogicPath: "folder/file.txt", PhysicalHash: "object", FileName: "file.txt",
		DestinationObject: "shares/file.txt", ShareURL: "https://example.test/file.txt", Status: status,
	}
	if completed {
		now := time.Now().UTC()
		record.CompletedAt = &now
	}
	record, err := store.CreateShare(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func newShareTestService(store MetadataStore, dispatcher Dispatcher, uploader ShareUploader, notifier ShareNotifier) *Service {
	options := []Option{WithDispatcher(dispatcher), WithUploader(uploader), WithNotifier(notifier)}
	return NewService(config.Config{
		ShareGCSBucket: "test-bucket", TelegramBotToken: "test-token", TelegramChatID: "test-chat",
	}, store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), options...)
}

func TestStartPersistsPendingBeforeDispatchFailure(t *testing.T) {
	store := openShareTestStore(t)
	createShareTestRecord(t, store, "share-start-failure", StatusDraft, false)
	dispatcher := &fakeDispatcher{err: errors.New("temporary dispatch outage")}
	service := newShareTestService(store, dispatcher, &countedUploader{}, &countedNotifier{})

	record, err := service.Start(context.Background(), "share-start-failure")
	if !errors.Is(err, ErrDispatchPending) || record.Status != StatusDraft {
		t.Fatalf("Start = %#v, err %v", record, err)
	}
	persisted, found, findErr := store.FindShare(context.Background(), record.ID)
	if findErr != nil || !found || persisted.DispatchStatus != DispatchPending || persisted.DispatchAttempts != 1 || persisted.NextDispatchAt == nil {
		t.Fatalf("persisted dispatch = %#v, found %t, err %v", persisted, found, findErr)
	}
	if persisted.Status != StatusDraft {
		t.Fatalf("business status changed before worker claim: %q", persisted.Status)
	}
}

func TestRelayRecoversRequestCommittedBeforeDispatch(t *testing.T) {
	store := openShareTestStore(t)
	createShareTestRecord(t, store, "share-crash-request", StatusDraft, false)
	now := time.Now().UTC()
	if _, _, err := store.RequestShareJob(context.Background(), "share-crash-request", "target", now); err != nil {
		t.Fatal(err)
	}
	dispatcher := &fakeDispatcher{}
	relay := NewRelay(store, dispatcher, slog.New(slog.NewTextHandler(io.Discard, nil)), WithRelayClock(func() time.Time { return now }))
	if err := relay.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, _, _ := store.FindShare(context.Background(), "share-crash-request")
	if persisted.DispatchStatus != DispatchDispatched || len(dispatcher.jobs) != 1 {
		t.Fatalf("recovered dispatch = %#v, jobs %d", persisted, len(dispatcher.jobs))
	}
}

func TestRelayPermanentFailureStopsAutomaticRetry(t *testing.T) {
	store := openShareTestStore(t)
	createShareTestRecord(t, store, "share-permanent", StatusDraft, false)
	now := time.Now().UTC().Add(-25 * time.Hour)
	_, _, _ = store.RequestShareJob(context.Background(), "share-permanent", "target", now)
	relay := NewRelay(store, &fakeDispatcher{err: Permanent(errors.New("missing topic"))}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithRelayClock(func() time.Time { return now }))
	if err := relay.RunOnce(context.Background()); err == nil || !IsPermanent(err) {
		t.Fatalf("RunOnce error = %v", err)
	}
	persisted, _, _ := store.FindShare(context.Background(), "share-permanent")
	if persisted.DispatchStatus != DispatchFailed || persisted.NextDispatchAt != nil || persisted.LastDispatchError == "" {
		t.Fatalf("permanent dispatch state = %#v", persisted)
	}
	// Manual Start is the explicit recovery path after configuration repair.
	dispatcher := &fakeDispatcher{}
	retryAt := time.Now().UTC()
	service := NewService(config.Config{
		ShareGCSBucket: "test-bucket", TelegramBotToken: "test-token", TelegramChatID: "test-chat",
	}, store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDispatcher(dispatcher), WithUploader(&countedUploader{}), WithNotifier(&countedNotifier{}),
		WithClock(func() time.Time { return retryAt }),
	)
	if _, err := service.Start(context.Background(), "share-permanent"); err != nil {
		t.Fatal(err)
	}
	persisted, _, _ = store.FindShare(context.Background(), "share-permanent")
	if persisted.DispatchStatus != DispatchDispatched || persisted.DispatchAttempts != 1 ||
		persisted.StartRequestedAt == nil || !persisted.StartRequestedAt.Equal(retryAt) {
		t.Fatalf("manual retry did not reset dispatch window = %#v", persisted)
	}
}

func TestProcessShareJobAcknowledgesActiveDuplicateAndTakesExpiredLease(t *testing.T) {
	store := openShareTestStore(t)
	createShareTestRecord(t, store, "share-lease", StatusDraft, false)
	if _, claimed, err := store.ClaimShareJob(context.Background(), "share-lease", "existing", time.Now().Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("prime lease claimed %t, err %v", claimed, err)
	}
	uploader, notifier := &countedUploader{}, &countedNotifier{}
	service := newShareTestService(store, &fakeDispatcher{}, uploader, notifier)
	if err := service.ProcessShareJob(context.Background(), Job{Version: JobVersion, ShareID: "share-lease"}); err != nil {
		t.Fatalf("active duplicate should ACK: %v", err)
	}
	if uploader.calls != 0 || notifier.calls != 0 {
		t.Fatalf("duplicate executed uploader=%d notifier=%d", uploader.calls, notifier.calls)
	}
	// Persist an already-expired lease and verify a new worker completes it.
	_ = store.ReleaseShareJob(context.Background(), "share-lease", "existing")
	if _, claimed, err := store.ClaimShareJob(context.Background(), "share-lease", "expired", time.Now().Add(time.Millisecond)); err != nil || !claimed {
		t.Fatalf("prime expiring lease claimed %t, err %v", claimed, err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := service.ProcessShareJob(context.Background(), Job{Version: JobVersion, ShareID: "share-lease"}); err != nil {
		t.Fatalf("expired lease takeover: %v", err)
	}
	persisted, _, _ := store.FindShare(context.Background(), "share-lease")
	if persisted.Status != StatusNotified || uploader.calls != 1 || notifier.calls != 1 {
		t.Fatalf("takeover result = %#v, uploader=%d notifier=%d", persisted, uploader.calls, notifier.calls)
	}
}

func TestProcessRecoversUploadBeforeCompletedStateWrite(t *testing.T) {
	base := openShareTestStore(t)
	createShareTestRecord(t, base, "share-upload-crash", StatusDraft, false)
	store := &failUploadedOnceStore{Store: base, remaining: 1}
	uploader, notifier := &countedUploader{}, &countedNotifier{}
	service := newShareTestService(store, &fakeDispatcher{}, uploader, notifier)
	job := Job{Version: JobVersion, ShareID: "share-upload-crash"}
	if err := service.ProcessShareJob(context.Background(), job); err == nil {
		t.Fatal("first process should observe injected state-write failure")
	}
	if err := service.ProcessShareJob(context.Background(), job); err != nil {
		t.Fatalf("retry process: %v", err)
	}
	persisted, _, _ := base.FindShare(context.Background(), job.ShareID)
	if persisted.Status != StatusNotified || persisted.CompletedAt == nil || uploader.calls != 2 || notifier.calls != 1 {
		t.Fatalf("recovery = %#v, uploader=%d notifier=%d", persisted, uploader.calls, notifier.calls)
	}
}

func TestCompletedAndNotificationFailureSkipUpload(t *testing.T) {
	store := openShareTestStore(t)
	createShareTestRecord(t, store, "share-completed", StatusUploading, true)
	uploader := &countedUploader{}
	notifier := &countedNotifier{err: errors.New("temporary telegram outage")}
	service := newShareTestService(store, &fakeDispatcher{}, uploader, notifier)
	job := Job{Version: JobVersion, ShareID: "share-completed"}
	if err := service.ProcessShareJob(context.Background(), job); err == nil {
		t.Fatal("first notification should fail")
	}
	persisted, _, _ := store.FindShare(context.Background(), job.ShareID)
	if persisted.Status != StatusNotifyFailed || persisted.CompletedAt == nil || uploader.calls != 0 {
		t.Fatalf("notification failure state = %#v, uploader=%d", persisted, uploader.calls)
	}
	notifier.err = nil
	if err := service.ProcessShareJob(context.Background(), job); err != nil {
		t.Fatalf("notification retry: %v", err)
	}
	persisted, _, _ = store.FindShare(context.Background(), job.ShareID)
	if persisted.Status != StatusNotified || uploader.calls != 0 || notifier.calls != 2 {
		t.Fatalf("notification retry = %#v, uploader=%d notifier=%d", persisted, uploader.calls, notifier.calls)
	}
}
