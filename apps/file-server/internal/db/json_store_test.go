package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestJSONStore(t *testing.T) Store {
	t.Helper()
	store, err := NewJSONLocal(filepath.Join(t.TempDir(), "nested", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestJSONLocalPersistsCompleteState(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "metadata.json")
	store, err := NewJSONLocal(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "/docs"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "/docs/a.txt", "object-a", 12); err != nil {
		t.Fatal(err)
	}
	share, err := store.CreateShare(ctx, ShareRecord{ID: "s1", LogicPath: "/docs/a.txt", Status: "draft"})
	if err != nil || share.ID != "s1" {
		t.Fatalf("CreateShare: %#v, %v", share, err)
	}
	upload, err := store.CreateUpload(ctx, UploadRecord{ID: "u1", LogicPath: "/docs/b.txt", PhysicalHash: "pending", Driver: "local", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || upload.ID != "u1" {
		t.Fatalf("CreateUpload: %#v, %v", upload, err)
	}
	lock, err := store.CreateDAVLock(ctx, DAVLockRecord{Token: "t1", Path: "/docs", Depth: -1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || lock.Token != "t1" {
		t.Fatalf("CreateDAVLock: %#v, %v", lock, err)
	}
	store.Close()

	reopened, err := NewJSONLocal(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := reopened.Find(ctx, "/docs/a.txt"); err != nil || !ok {
		t.Fatalf("Find persisted file: ok=%v err=%v", ok, err)
	}
	if _, ok, err := reopened.FindShare(ctx, "s1"); err != nil || !ok {
		t.Fatalf("Find persisted share: ok=%v err=%v", ok, err)
	}
	if _, ok, err := reopened.FindUpload(ctx, "u1"); err != nil || !ok {
		t.Fatalf("Find persisted upload: ok=%v err=%v", ok, err)
	}
	if _, ok, err := reopened.FindDAVLock(ctx, "t1"); err != nil || !ok {
		t.Fatalf("Find persisted lock: ok=%v err=%v", ok, err)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata mode = %v", info.Mode().Perm())
	}
}

func TestJSONConditionalReplaceAndConcurrentUpdates(t *testing.T) {
	ctx := context.Background()
	store := newTestJSONStore(t)
	if err := store.UpsertFile(ctx, "/a", "v1", 1); err != nil {
		t.Fatal(err)
	}
	wrong := "wrong"
	if _, matched, err := store.ReplaceFileConditional(ctx, "/a", "v2", 2, &wrong, false); err != nil || matched {
		t.Fatalf("wrong CAS matched=%v err=%v", matched, err)
	}
	expected := "v1"
	if old, matched, err := store.ReplaceFileConditional(ctx, "/a", "v2", 2, &expected, false); err != nil || !matched || old != "v1" {
		t.Fatalf("CAS old=%q matched=%v err=%v", old, matched, err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.UpsertFile(ctx, "/f"+string(rune('a'+i)), "x", int64(i)); err != nil {
				t.Errorf("concurrent upsert: %v", err)
			}
		}(i)
	}
	wg.Wait()
	all, err := store.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 21 {
		t.Fatalf("file count=%d want 21", len(all))
	}
}

func TestJSONShareLeaseIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := newTestJSONStore(t)
	if _, err := store.CreateShare(ctx, ShareRecord{ID: "s1", Status: "draft"}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	if _, ok, err := store.ClaimShareJob(ctx, "s1", "worker-a", until); err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.ClaimShareJob(ctx, "s1", "worker-b", until); err != nil || ok {
		t.Fatalf("competing claim ok=%v err=%v", ok, err)
	}
	if err := store.ReleaseShareJob(ctx, "s1", "worker-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimShareJob(ctx, "s1", "worker-b", until); err != nil || !ok {
		t.Fatalf("claim after release ok=%v err=%v", ok, err)
	}
}

type conflictOnceBackend struct {
	mu         sync.Mutex
	data       []byte
	generation int64
	saves      int
}

func (b *conflictOnceBackend) Load(context.Context) ([]byte, int64, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...), b.generation, len(b.data) > 0, nil
}
func (b *conflictOnceBackend) Save(_ context.Context, data []byte, _ int64, _ bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.saves++
	if b.saves == 1 {
		return ErrJSONConflict
	}
	b.data = append([]byte(nil), data...)
	b.generation++
	return nil
}
func (*conflictOnceBackend) Close() error { return nil }

func TestJSONRetriesCASConflict(t *testing.T) {
	b := &conflictOnceBackend{}
	s := &JSONStore{backend: b}
	if err := s.UpsertFile(context.Background(), "/a", "x", 1); err != nil {
		t.Fatal(err)
	}
	if b.saves != 2 {
		t.Fatalf("save attempts=%d want 2", b.saves)
	}
	if _, ok, err := s.Find(context.Background(), "/a"); err != nil || !ok {
		t.Fatalf("find after retry ok=%v err=%v", ok, err)
	}
}

func TestDecodeRejectsUnknownVersion(t *testing.T) {
	_, err := decodeJSONState([]byte(`{"version":999}`), true)
	if err == nil || errors.Is(err, ErrJSONConflict) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJSONBatchMoveAndTrashLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newTestJSONStore(t)
	if err := store.UpsertDirectory(ctx, "/source"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "/source/a.txt", "a", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDirectory(ctx, "/target"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchMove(ctx, []string{"/source"}, "/target"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Find(ctx, "/target/source/a.txt"); !ok {
		t.Fatal("moved child not found")
	}

	trashed, err := store.TrashPaths(ctx, []TrashPath{{Path: "/target/source", TrashID: "trash-1"}})
	if err != nil || len(trashed) != 2 {
		t.Fatalf("TrashPaths records=%d err=%v", len(trashed), err)
	}
	if _, ok, _ := store.Find(ctx, "/target/source"); ok {
		t.Fatal("trash leaked into active Find")
	}
	if err := store.UpsertDirectory(ctx, "/target/source"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenamePath(ctx, "/target/source", "/target/recreated"); err != nil {
		t.Fatal(err)
	}
	trashRecords, err := store.ListTrashRecords(ctx, []string{"trash-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trashRecords {
		if !strings.HasPrefix(record.LogicPath, "/target/source") {
			t.Fatalf("trashed record renamed: %s", record.LogicPath)
		}
	}
	if _, err := store.RestoreTrash(ctx, []string{"trash-1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Find(ctx, "/target/source/a.txt"); !ok {
		t.Fatal("restored child not found")
	}
}

func TestJSONRestorePreflightRejectsDuplicateTrashPathsAtomically(t *testing.T) {
	ctx := context.Background()
	store := newTestJSONStore(t)
	if err := store.UpsertFile(ctx, "/a", "one", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrashPaths(ctx, []TrashPath{{Path: "/a", TrashID: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "/a", "two", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrashPaths(ctx, []TrashPath{{Path: "/a", TrashID: "two"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreTrash(ctx, []string{"one", "two"}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("RestoreTrash err=%v", err)
	}
	if records, _ := store.ListTrashRecords(ctx, nil); len(records) != 2 {
		t.Fatalf("trash records=%d want 2", len(records))
	}
	if _, ok, _ := store.Find(ctx, "/a"); ok {
		t.Fatal("restore partially applied")
	}
}

func TestJSONTrashClaimIsIdempotentAndRestorationIsBlocked(t *testing.T) {
	ctx := context.Background()
	store := newTestJSONStore(t)
	if err := store.UpsertFile(ctx, "/a", "one", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrashPaths(ctx, []TrashPath{{Path: "/a", TrashID: "trash"}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTrash(ctx, []string{"trash"})
	if err != nil || len(claimed) != 1 || !claimed[0].TrashDeleting {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if claimedAgain, err := store.ClaimTrash(ctx, []string{"trash"}); err != nil || len(claimedAgain) != 1 {
		t.Fatalf("second claim=%#v err=%v", claimedAgain, err)
	}
	if _, err := store.RestoreTrash(ctx, []string{"trash"}); !errors.Is(err, ErrTrashBusy) {
		t.Fatalf("restore err=%v", err)
	}
	if deleted, err := store.DeleteTrash(ctx, []string{"trash"}); err != nil || deleted != 1 {
		t.Fatalf("delete claimed trash=%d err=%v", deleted, err)
	}
}
