package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestTreeV4(t *testing.T, options TreeV4Options) *TreeStore {
	t.Helper()
	store, err := NewTreeLocalV4(t.TempDir(), "test-v4", options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	tree := store.(*TreeStore)
	if err = tree.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestTreeV4StrongPathCreateReplaceRenameAndMove(t *testing.T) {
	ctx := context.Background()
	store := newTestTreeV4(t, TreeV4Options{ShardCount: 8})
	for _, directory := range []string{"source", "source/nested", "target"} {
		if err := store.UpsertDirectory(ctx, directory); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
	}
	if previous, matched, err := store.ReplaceFileConditional(ctx, "source/nested/a.txt", "object-a", 7, nil, true); err != nil || !matched || previous != "" {
		t.Fatalf("create previous=%q matched=%t err=%v", previous, matched, err)
	}
	record, found, err := store.Find(ctx, "source/nested/a.txt")
	if err != nil || !found || record.PhysicalHash != "object-a" || record.ID <= 0 {
		t.Fatalf("created record=%+v found=%t err=%v", record, found, err)
	}
	legacyID := record.ID
	expected := "object-a"
	if previous, matched, err := store.ReplaceFileConditional(ctx, "source/nested/a.txt", "object-b", 9, &expected, false); err != nil || !matched || previous != "object-a" {
		t.Fatalf("replace previous=%q matched=%t err=%v", previous, matched, err)
	}
	if err = store.RenamePath(ctx, "source/nested", "source/renamed"); err != nil {
		t.Fatalf("rename directory: %v", err)
	}
	if _, found, err = store.Find(ctx, "source/nested/a.txt"); err != nil || found {
		t.Fatalf("old path found=%t err=%v", found, err)
	}
	record, found, err = store.Find(ctx, "source/renamed/a.txt")
	if err != nil || !found || record.ID != legacyID || record.PhysicalHash != "object-b" {
		t.Fatalf("renamed child=%+v found=%t err=%v", record, found, err)
	}
	moved, err := store.BatchMove(ctx, []string{"source/renamed"}, "target")
	if err != nil || len(moved) != 1 || moved[0].LogicPath != "target/renamed" {
		t.Fatalf("move=%+v err=%v", moved, err)
	}
	record, found, err = store.Find(ctx, "target/renamed/a.txt")
	if err != nil || !found || record.ID != legacyID {
		t.Fatalf("moved child=%+v found=%t err=%v", record, found, err)
	}
	page, err := store.ListDirectChildren(ctx, "target/renamed", DirectChildrenOptions{})
	if err != nil || page.Total != 1 || page.Records[0].LogicPath != "target/renamed/a.txt" {
		t.Fatalf("listing=%+v err=%v", page, err)
	}
}

func TestTreeV4SameParentRenameSkipsDerivedQueueAndArchives(t *testing.T) {
	ctx := context.Background()
	store := newTestTreeV4(t, TreeV4Options{ShardCount: 8})
	if _, _, err := store.ReplaceFileConditional(ctx, "a.txt", "object-a", 7, nil, true); err != nil {
		t.Fatal(err)
	}
	store.v4.waitFinalizers()
	if _, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{RebuildDirectories: store.rebuildV4DirectorySummaries}); err != nil {
		t.Fatal(err)
	}
	if err := store.RenamePath(ctx, "a.txt", "b.txt"); err != nil {
		t.Fatal(err)
	}
	store.v4.waitFinalizers()
	if deltas, err := store.objects.List(ctx, store.treeDerivedDeltaPrefix()); err != nil || len(deltas) != 0 {
		t.Fatalf("derived deltas=%v err=%v", deltas, err)
	}
	if active, err := store.objects.List(ctx, store.prefix+"/v4/transactions/active/"); err != nil || len(active) != 0 {
		t.Fatalf("active transactions=%v err=%v", active, err)
	}
	if record, found, err := store.Find(ctx, "b.txt"); err != nil || !found || record.PhysicalHash != "object-a" {
		t.Fatalf("renamed record=%+v found=%t err=%v", record, found, err)
	}
}

func TestTreeV4ConcurrentDisjointWrites(t *testing.T) {
	ctx := context.Background()
	store := newTestTreeV4(t, TreeV4Options{ShardCount: 64})
	const writers = 24
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for index := 0; index < writers; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := store.ReplaceFileConditional(ctx, fmt.Sprintf("file-%02d.bin", index), fmt.Sprintf("object-%02d", index), int64(index+1), nil, true)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListDirectChildren(ctx, "", DirectChildrenOptions{})
	if err != nil || page.Total != writers {
		paths := make([]string, 0, len(page.Records))
		for _, record := range page.Records {
			paths = append(paths, record.LogicPath)
		}
		t.Fatalf("total=%d paths=%v err=%v", page.Total, paths, err)
	}
}

func TestTreeV4ConcurrentWritesRetrySharedShardConflicts(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &observeV4WritesBackend{treeBackend: local, delay: 100 * time.Millisecond}
	store, err := newTreeStoreV4(backend, "shared-shard-retry-v4", TreeV4Options{ShardCount: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, 12)
	for candidate := 0; len(names) < cap(names); candidate++ {
		name := fmt.Sprintf("collision-%04d.bin", candidate)
		if store.v4.shardFor(name) == 0 {
			names = append(names, name)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, len(names))
	var wg sync.WaitGroup
	for _, name := range names {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, writeErr := store.ReplaceFileConditional(ctx, name, "object-"+name, 1, nil, true)
			errs <- writeErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for writeErr := range errs {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	page, err := store.ListDirectChildren(ctx, "", DirectChildrenOptions{})
	if err != nil || page.Total != len(names) {
		t.Fatalf("total=%d want=%d err=%v", page.Total, len(names), err)
	}
}

type failV4CommitBackend struct {
	treeBackend
	mu       sync.Mutex
	failNext bool
}

type applyThenFailV4CommitBackend struct {
	treeBackend
	mu       sync.Mutex
	failNext bool
}

type failV4PromotionBackend struct {
	treeBackend
	mu       sync.Mutex
	failNext bool
}

type blockingV4PromotionBackend struct {
	treeBackend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (backend *blockingV4PromotionBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	if strings.Contains(key, "/v4/directories/") {
		var envelope treeV4Envelope
		if json.Unmarshal(data, &envelope) == nil && envelope.Pending == nil && len(envelope.Current) != 0 {
			backend.once.Do(func() { close(backend.entered) })
			select {
			case <-backend.release:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
	}
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func TestTreeV4CommittedMutationReturnsBeforeParticipantPromotion(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &blockingV4PromotionBackend{
		treeBackend: local,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	store, err := newTreeStoreV4(backend, "async-finalize-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	returned := make(chan error, 1)
	go func() {
		_, _, writeErr := store.ReplaceFileConditional(ctx, "a.txt", "object-a", 1, nil, true)
		returned <- writeErr
	}()
	select {
	case <-backend.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("background participant promotion did not start")
	}
	select {
	case writeErr := <-returned:
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("committed mutation waited for participant promotion")
	}
	close(backend.release)
	store.v4.waitFinalizers()
	record, found, err := store.Find(ctx, "a.txt")
	if err != nil || !found || record.PhysicalHash != "object-a" {
		t.Fatalf("record=%+v found=%t err=%v", record, found, err)
	}
}

func (backend *failV4PromotionBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.failNext && strings.Contains(key, "/v4/directories/") {
		var envelope treeV4Envelope
		if json.Unmarshal(data, &envelope) == nil && envelope.Pending == nil && len(envelope.Current) != 0 {
			backend.failNext = false
			return 0, fmt.Errorf("injected participant promotion failure")
		}
	}
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func TestTreeV4ReaderResolvesArchivedCommitAfterPromotionFailure(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &failV4PromotionBackend{treeBackend: local}
	store, err := newTreeStoreV4(backend, "promotion-failure-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	backend.failNext = true
	if _, matched, err := store.ReplaceFileConditional(ctx, "a.txt", "object-a", 1, nil, true); err != nil || !matched {
		t.Fatalf("write matched=%t err=%v", matched, err)
	}
	store.v4.waitFinalizers()
	if _, err = store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{RebuildDirectory: func(context.Context, string) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if active, listErr := store.objects.List(ctx, store.prefix+"/v4/transactions/active/"); listErr != nil || len(active) != 0 {
		t.Fatalf("active=%v err=%v", active, listErr)
	}
	if _, err = RecoverTreeV4Transactions(ctx, store); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Find(ctx, "a.txt")
	if err != nil || !found || record.PhysicalHash != "object-a" {
		t.Fatalf("record=%+v found=%t err=%v", record, found, err)
	}
}

func (backend *applyThenFailV4CommitBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.failNext && strings.Contains(key, "/v4/transactions/active/") {
		var transaction treeV4Transaction
		if json.Unmarshal(data, &transaction) == nil && transaction.Status == "committed" {
			backend.failNext = false
			if _, err := backend.treeBackend.Put(ctx, key, data, expected); err != nil {
				return 0, err
			}
			return 0, fmt.Errorf("injected response loss after commit")
		}
	}
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func TestTreeV4CommitResponseLossIsResolvedByReadBack(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &applyThenFailV4CommitBackend{treeBackend: local}
	store, err := newTreeStoreV4(backend, "ambiguous-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	backend.failNext = true
	if _, matched, writeErr := store.ReplaceFileConditional(ctx, "a.txt", "new", 2, nil, true); writeErr != nil || !matched {
		t.Fatalf("committed write reported matched=%t err=%v", matched, writeErr)
	}
	record, found, err := store.Find(ctx, "a.txt")
	if err != nil || !found || record.PhysicalHash != "new" {
		t.Fatalf("record=%+v found=%t err=%v", record, found, err)
	}
}

func TestTreeV4MarkDerivedAppliedIsIdempotentAfterArchive(t *testing.T) {
	ctx := context.Background()
	store := newTestTreeV4(t, TreeV4Options{ShardCount: 4})
	if _, _, err := store.ReplaceFileConditional(ctx, "a.txt", "object-a", 1, nil, true); err != nil {
		t.Fatal(err)
	}
	store.v4.waitFinalizers()
	active, err := store.objects.List(ctx, store.prefix+"/v4/transactions/active/")
	if err != nil || len(active) != 1 {
		t.Fatalf("active=%v err=%v", active, err)
	}
	object, found, err := store.objects.Get(ctx, active[0])
	if err != nil || !found {
		t.Fatalf("manifest found=%t err=%v", found, err)
	}
	var transaction treeV4Transaction
	if err = json.Unmarshal(object.Data, &transaction); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{RebuildDirectory: func(context.Context, string) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if err = MarkTreeV4DerivedApplied(ctx, store, transaction.ID); err != nil {
		t.Fatalf("repeat mark: %v", err)
	}
	if _, found, err = store.objects.Get(ctx, store.v4.transactionKey(transaction.ID)); err != nil || found {
		t.Fatalf("active transaction recreated found=%t err=%v", found, err)
	}
	if _, found, err = store.objects.Get(ctx, store.v4.journalKey(transaction.ID)); err != nil || !found {
		t.Fatalf("journal found=%t err=%v", found, err)
	}
}

type abortCommitRaceV4Backend struct {
	treeBackend
	mu             sync.Mutex
	failCommitOnce bool
	raceAbortOnce  bool
}

type takeoverOperationBackend struct {
	treeBackend
	mu    sync.Mutex
	key   string
	reads int
	armed bool
}

func (backend *takeoverOperationBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.armed && key == backend.key {
		backend.reads++
		if backend.reads == 2 {
			object, found, err := backend.treeBackend.Get(ctx, key)
			if err != nil || !found {
				return object, found, err
			}
			var operation OperationRecord
			if err = json.Unmarshal(object.Data, &operation); err != nil {
				return treeObject{}, false, err
			}
			operation.Status = "running"
			operation.LeaseOwner = "takeover-owner"
			until := time.Now().UTC().Add(time.Minute)
			operation.LeaseUntil = &until
			payload, _ := marshalTree(operation)
			if _, err = backend.treeBackend.Put(ctx, key, payload, &object.Generation); err != nil {
				return treeObject{}, false, err
			}
		}
	}
	return backend.treeBackend.Get(ctx, key)
}

func TestTreeV4OperationLeaseRejectsLiveRunnerAndOldOwnerCannotOverwrite(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &takeoverOperationBackend{treeBackend: local}
	store, err := newTreeStoreV4(backend, "operation-lease-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertDirectory(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ReplaceFileConditional(ctx, "a.txt", "a", 1, nil, true); err != nil {
		t.Fatal(err)
	}
	operation, err := store.CreateMoveOperation(ctx, []string{"a.txt"}, "target")
	if err != nil {
		t.Fatal(err)
	}
	loaded, generation, found, err := store.loadOperation(ctx, operation.ID)
	if err != nil || !found {
		t.Fatalf("operation found=%t err=%v", found, err)
	}
	loaded.Status = "running"
	loaded.LeaseOwner = "live-owner"
	until := time.Now().UTC().Add(time.Minute)
	loaded.LeaseUntil = &until
	if _, err = store.saveOperationCAS(ctx, loaded, generation); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RunOperation(ctx, operation.ID); err == nil {
		t.Fatal("live operation lease was not rejected")
	}
	loaded, generation, _, err = store.loadOperation(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.LeaseUntil = func() *time.Time { expired := time.Now().UTC().Add(-time.Minute); return &expired }()
	if _, err = store.saveOperationCAS(ctx, loaded, generation); err != nil {
		t.Fatal(err)
	}
	backend.key, backend.armed, backend.reads = store.operationKey(operation.ID), true, 0
	if _, err = store.RunOperation(ctx, operation.ID); err == nil || !strings.Contains(err.Error(), "operation lease lost") {
		t.Fatalf("old runner err=%v", err)
	}
	current, _, _, err := store.loadOperation(ctx, operation.ID)
	if err != nil || current.LeaseOwner != "takeover-owner" || current.Status != "running" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestTreeV4OperationMarksPermanentRaceLoserFailed(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := newTreeStoreV4(local, "operation-race-loser-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ReplaceFileConditional(ctx, "a.txt", "hash", 1, nil, true); err != nil {
		t.Fatal(err)
	}
	winner, err := store.CreateRenameOperation(ctx, "a.txt", "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	loser, err := store.CreateRenameOperation(ctx, "a.txt", "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if winner, err = store.RunOperation(ctx, winner.ID); err != nil || winner.Status != "completed" {
		t.Fatalf("winner=%+v err=%v", winner, err)
	}
	if loser, err = store.RunOperation(ctx, loser.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("loser=%+v err=%v, want ErrNotFound", loser, err)
	}
	if loser.Status != "failed" || loser.LeaseOwner != "" || loser.LeaseUntil != nil {
		t.Fatalf("loser did not reach a lease-free terminal state: %+v", loser)
	}
	if rerun, rerunErr := store.RunOperation(ctx, loser.ID); rerunErr != nil || rerun.Status != "failed" {
		t.Fatalf("terminal loser rerun=%+v err=%v", rerun, rerunErr)
	}
}

func (backend *abortCommitRaceV4Backend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if strings.Contains(key, "/v4/transactions/active/") {
		var transaction treeV4Transaction
		if json.Unmarshal(data, &transaction) == nil {
			if backend.failCommitOnce && transaction.Status == "committed" {
				backend.failCommitOnce = false
				return 0, fmt.Errorf("injected pre-commit response failure")
			}
			if backend.raceAbortOnce && transaction.Status == "aborted" {
				backend.raceAbortOnce = false
				object, found, err := backend.treeBackend.Get(ctx, key)
				if err != nil || !found {
					return 0, err
				}
				var live treeV4Transaction
				if err = json.Unmarshal(object.Data, &live); err != nil {
					return 0, err
				}
				live.Status = "committed"
				live.LeaseUntil = time.Time{}
				payload, _ := marshalTree(live)
				if _, err = backend.treeBackend.Put(ctx, key, payload, &object.Generation); err != nil {
					return 0, err
				}
				return 0, ErrMetadataConflict
			}
		}
	}
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func TestTreeV4RecoveryRereadsManifestWhenAbortLosesToCommit(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &abortCommitRaceV4Backend{treeBackend: local, failCommitOnce: true}
	store, err := newTreeStoreV4(backend, "abort-race-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ReplaceFileConditional(ctx, "a.txt", "new", 2, nil, true); err == nil {
		t.Fatal("write unexpectedly reported success")
	}
	backend.raceAbortOnce = true
	store.v4.now = func() time.Time { return time.Now().UTC().Add(10 * time.Minute) }
	if recovered, recoverErr := RecoverTreeV4Transactions(ctx, store); recoverErr != nil || recovered == 0 {
		t.Fatalf("recovered=%d err=%v", recovered, recoverErr)
	}
	record, found, err := store.Find(ctx, "a.txt")
	if err != nil || !found || record.PhysicalHash != "new" {
		t.Fatalf("committed race record=%+v found=%t err=%v", record, found, err)
	}
}

type advancingLeaseClockBackend struct {
	treeBackend
	mu      sync.Mutex
	now     time.Time
	advance bool
}

type injectAfterGetBackend struct {
	treeBackend
	mu       sync.Mutex
	key      string
	callback func() error
}

type childTrashBarrierBackend struct {
	treeBackend
	mu            sync.Mutex
	targetKey     string
	armed         bool
	held          bool
	leaseAcquired chan struct{}
	contenderSeen chan struct{}
	release       chan struct{}
}

func (backend *childTrashBarrierBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	backend.mu.Lock()
	block := backend.armed && key == backend.targetKey && !backend.held
	if block {
		backend.held = true
	}
	backend.mu.Unlock()
	generation, err := backend.treeBackend.Put(ctx, key, data, expected)
	if block && err == nil {
		close(backend.leaseAcquired)
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-backend.release:
		}
	}
	return generation, err
}

func (backend *childTrashBarrierBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	object, found, err := backend.treeBackend.Get(ctx, key)
	backend.mu.Lock()
	contended := backend.armed && backend.held && key == backend.targetKey
	backend.mu.Unlock()
	if contended {
		select {
		case <-backend.contenderSeen:
		default:
			close(backend.contenderSeen)
		}
	}
	return object, found, err
}

func (backend *injectAfterGetBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	object, found, err := backend.treeBackend.Get(ctx, key)
	backend.mu.Lock()
	callback := backend.callback
	if key == backend.key {
		backend.callback = nil
	} else {
		callback = nil
	}
	backend.mu.Unlock()
	if callback != nil {
		if callbackErr := callback(); callbackErr != nil {
			return treeObject{}, false, callbackErr
		}
	}
	return object, found, err
}

func TestTreeV4DeleteAbsentDoesNotDeleteDirectoryCreatedAfterInitialRead(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &injectAfterGetBackend{treeBackend: local}
	store, err := newTreeStoreV4(backend, "delete-absent-v4", TreeV4Options{ShardCount: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	backend.key = store.v4.shardKey("root", store.v4.shardFor("newdir"))
	backend.callback = func() error { return store.UpsertDirectory(ctx, "newdir") }
	if err = store.DeletePath(ctx, "newdir"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Find(ctx, "newdir"); err != nil || !found {
		t.Fatalf("concurrent directory found=%t err=%v", found, err)
	}
}

func TestTreeV4ConcurrentChildCreateAndTrashNeverLeavesOrphan(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &childTrashBarrierBackend{treeBackend: local, leaseAcquired: make(chan struct{}), contenderSeen: make(chan struct{}), release: make(chan struct{})}
	store, err := newTreeStoreV4(backend, "child-trash-race-v4", TreeV4Options{ShardCount: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertDirectory(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	directory, found, err := store.v4.resolve(ctx, "dir")
	if err != nil || !found {
		t.Fatalf("directory found=%t err=%v", found, err)
	}
	resource := fmt.Sprintf("directory:%s:shard:%03d", directory.NodeID, store.v4.shardFor("child.txt"))
	backend.targetKey, backend.armed = store.v4.leaseKey(resource), true
	createErr := make(chan error, 1)
	go func() {
		_, _, writeErr := store.ReplaceFileConditional(ctx, "dir/child.txt", "child", 1, nil, true)
		createErr <- writeErr
	}()
	<-backend.leaseAcquired
	trashErr := make(chan error, 1)
	go func() {
		_, operationErr := store.TrashPaths(ctx, []TrashPath{{Path: "dir", TrashID: "race-trash"}})
		trashErr <- operationErr
	}()
	select {
	case <-backend.contenderSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("trash did not contend on child shard lease")
	}
	close(backend.release)
	if err = <-createErr; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err = <-trashErr; err != nil {
		t.Fatalf("trash: %v", err)
	}
	if _, found, err = store.Find(ctx, "dir/child.txt"); err != nil || found {
		t.Fatalf("active orphan found=%t err=%v", found, err)
	}
	records, err := store.ListTrashRecords(ctx, []string{"race-trash"})
	if err != nil {
		t.Fatal(err)
	}
	childFound := false
	for _, record := range records {
		childFound = childFound || record.LogicPath == "dir/child.txt"
	}
	if !childFound {
		t.Fatalf("trashed subtree missing committed child: %+v", records)
	}
}

func TestTreeV4CreateThenTrashBeforeReductionConvergesStatsToZero(t *testing.T) {
	ctx := context.Background()
	store := newTestTreeV4(t, TreeV4Options{ShardCount: 8})
	if err := store.UpsertDirectory(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReplaceFileConditional(ctx, "dir/child.txt", "child", 9, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrashPaths(ctx, []TrashPath{{Path: "dir", TrashID: "stats-trash"}}); err != nil {
		t.Fatal(err)
	}
	store.v4.waitFinalizers()
	if _, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{RebuildDirectory: func(context.Context, string) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	stats, err := store.MetadataStats(ctx)
	if err != nil || stats.LogicalFiles != 0 || stats.LogicalDirs != 0 || stats.LogicalBytes != 0 || stats.PhysicalObjects != 0 || stats.PhysicalBytes != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func (backend *advancingLeaseClockBackend) tick(key string) {
	if strings.Contains(key, "/v4/leases/") && backend.advance {
		backend.mu.Lock()
		backend.now = backend.now.Add(100 * time.Millisecond)
		backend.mu.Unlock()
	}
}

func (backend *advancingLeaseClockBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	backend.tick(key)
	return backend.treeBackend.Get(ctx, key)
}

func (backend *advancingLeaseClockBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	backend.tick(key)
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func (backend *advancingLeaseClockBackend) current() time.Time {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.now
}

func TestTreeV4LargeDirectoryTrashBudgetsLeaseAcquisitionTime(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &advancingLeaseClockBackend{treeBackend: local, now: time.Now().UTC()}
	store, err := newTreeStoreV4(backend, "large-trash-v4", TreeV4Options{ShardCount: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	store.v4.now = backend.current
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	path := ""
	for index := 0; index < 12; index++ {
		path = joinLogicPath(path, fmt.Sprintf("d%02d", index))
		if err = store.UpsertDirectory(ctx, path); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	backend.advance = true
	if _, err = store.TrashPaths(ctx, []TrashPath{{Path: "d00", TrashID: "large-trash"}}); err != nil {
		t.Fatalf("trash large directory: %v", err)
	}
}

type observeV4WritesBackend struct {
	treeBackend
	mu        sync.Mutex
	active    int
	maxActive int
	delay     time.Duration
}

func (backend *observeV4WritesBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	observed := strings.Contains(key, "/v4/directories/")
	if observed {
		var envelope treeV4Envelope
		observed = json.Unmarshal(data, &envelope) == nil && envelope.Pending != nil
	}
	if observed {
		backend.mu.Lock()
		backend.active++
		if backend.active > backend.maxActive {
			backend.maxActive = backend.active
		}
		backend.mu.Unlock()
		time.Sleep(backend.delay)
		defer func() {
			backend.mu.Lock()
			backend.active--
			backend.mu.Unlock()
		}()
	}
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func (backend *observeV4WritesBackend) reset() {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.maxActive = 0
}

func (backend *observeV4WritesBackend) maximum() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.maxActive
}

func TestTreeV4ScopedLeasesAllowDisjointWritesAndSerializeSamePath(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &observeV4WritesBackend{treeBackend: local, delay: 15 * time.Millisecond}
	store, err := newTreeStoreV4(backend, "observed-v4", TreeV4Options{ShardCount: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	names := []string{"a.bin", "b.bin", "c.bin", "d.bin", "e.bin", "f.bin", "g.bin", "h.bin"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, len(names))
	for _, name := range names {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, writeErr := store.ReplaceFileConditional(ctx, name, "object-"+name, 1, nil, true)
			errs <- writeErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for writeErr := range errs {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if maximum := backend.maximum(); maximum <= 1 {
		t.Fatalf("disjoint writes max concurrency = %d, want > 1", maximum)
	}

	backend.reset()
	start = make(chan struct{})
	errs = make(chan error, len(names))
	for index := range names {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, writeErr := store.ReplaceFileConditional(ctx, "hot.bin", fmt.Sprintf("hot-%d", index), int64(index), nil, false)
			errs <- writeErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for writeErr := range errs {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if maximum := backend.maximum(); maximum != 1 {
		t.Fatalf("same-path writes max concurrency = %d, want 1", maximum)
	}
	if _, found, findErr := store.Find(ctx, "hot.bin"); findErr != nil || !found {
		t.Fatalf("hot path found=%t err=%v", found, findErr)
	}
}

func (backend *failV4CommitBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.failNext && strings.Contains(key, "/v4/transactions/") {
		var transaction treeV4Transaction
		if json.Unmarshal(data, &transaction) == nil && transaction.Status == "committed" {
			backend.failNext = false
			return 0, fmt.Errorf("injected commit failure")
		}
	}
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func TestTreeV4FailedCommitKeepsPreviousValueAndRecovers(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &failV4CommitBackend{treeBackend: local}
	store, err := newTreeStoreV4(backend, "failure-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ReplaceFileConditional(ctx, "a.txt", "old", 1, nil, true); err != nil {
		t.Fatal(err)
	}
	backend.failNext = true
	expected := "old"
	if _, _, err = store.ReplaceFileConditional(ctx, "a.txt", "new", 2, &expected, false); err == nil {
		t.Fatal("replace unexpectedly committed")
	}
	record, found, err := store.Find(ctx, "a.txt")
	if err != nil || !found || record.PhysicalHash != "old" {
		t.Fatalf("visible after failed commit=%+v found=%t err=%v", record, found, err)
	}
	store.v4.now = func() time.Time { return time.Now().UTC().Add(time.Minute) }
	if recovered, recoverErr := RecoverTreeV4Transactions(ctx, store); recoverErr != nil || recovered == 0 {
		t.Fatalf("recovered=%d err=%v", recovered, recoverErr)
	}
	record, found, err = store.Find(ctx, "a.txt")
	if err != nil || !found || record.PhysicalHash != "old" {
		t.Fatalf("visible after recovery=%+v found=%t err=%v", record, found, err)
	}
}

func TestBulkImportAndExportTreeV4PreservesIDsTrashAndDerivedBaseline(t *testing.T) {
	ctx := context.Background()
	store, err := NewTreeLocalV4(t.TempDir(), "migration-v4", TreeV4Options{ShardCount: 8})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshot := TreeImportSnapshot{
		SourceSHA256: "source-hash",
		Records: []FileRecord{
			{ID: 10, LogicPath: "docs", IsDirectory: true, UpdatedAt: now},
			{ID: 11, LogicPath: "docs/a.txt", PhysicalHash: "object-a", Size: 7, UpdatedAt: now},
			{ID: 12, LogicPath: "gone.txt", PhysicalHash: "object-gone", Size: 3, UpdatedAt: now, TrashedAt: &now, TrashID: "trash-1", TrashRoot: true},
		},
		Shares:     []ShareRecord{{ID: "share-1", LogicPath: "docs/a.txt", Status: "pending"}},
		Operations: []OperationRecord{{ID: "operation-1", Type: "move", Status: "completed", CreatedAt: now, UpdatedAt: now}},
	}
	validation, err := BulkImportTreeV4(ctx, store, snapshot)
	if err != nil || validation.Active != 2 || validation.Trash != 1 {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
	record, found, err := store.Find(ctx, "docs/a.txt")
	if err != nil || !found || record.ID != 11 || record.PhysicalHash != "object-a" {
		t.Fatalf("record=%+v found=%t err=%v", record, found, err)
	}
	stats, err := store.(MetadataStatsProvider).MetadataStats(ctx)
	if err != nil || stats.LogicalFiles != 1 || stats.LogicalDirs != 1 || stats.LogicalBytes != 7 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	root, err := store.ListDirectChildren(ctx, "", DirectChildrenOptions{})
	if err != nil || root.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 7}) {
		t.Fatalf("root=%+v err=%v", root, err)
	}
	exported, err := ExportTreeV4(ctx, store)
	if err != nil || len(exported.Records) != 3 || len(exported.Shares) != 1 || len(exported.Operations) != 1 {
		t.Fatalf("export records=%d shares=%d operations=%d err=%v", len(exported.Records), len(exported.Shares), len(exported.Operations), err)
	}
	ids := map[int]bool{}
	for _, item := range exported.Records {
		ids[item.ID] = true
	}
	if !ids[10] || !ids[11] || !ids[12] {
		t.Fatalf("exported ids=%v", ids)
	}
	trashRecords, err := store.(*TreeStore).ListTrashRecords(ctx, []string{"trash-1"})
	if err != nil || len(trashRecords) != 1 || trashRecords[0].ID != 12 {
		t.Fatalf("trash records=%+v err=%v", trashRecords, err)
	}
	if _, err = store.(*TreeStore).RestoreTrash(ctx, []string{"trash-1"}); err != nil {
		t.Fatalf("restore imported trash: %v", err)
	}
	restored, found, err := store.Find(ctx, "gone.txt")
	if err != nil || !found || restored.ID != 12 || restored.PhysicalHash != "object-gone" {
		t.Fatalf("restored=%+v found=%t err=%v", restored, found, err)
	}
}

type dropV4TrashManifestBackend struct{ treeBackend }

func (backend *dropV4TrashManifestBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	if strings.Contains(key, "/v4/trash/manifests/") {
		return localGeneration(data), nil
	}
	return backend.treeBackend.Put(ctx, key, data, expected)
}

func TestBulkImportTreeV4DoesNotPublishCompletionWhenTrashReadBackIsMissing(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &dropV4TrashManifestBackend{treeBackend: local}
	store, err := newTreeStoreV4(backend, "missing-trash-v4", TreeV4Options{ShardCount: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	now := time.Now().UTC()
	snapshot := TreeImportSnapshot{SourceSHA256: "missing-trash", Records: []FileRecord{{
		ID: 1, LogicPath: "gone.txt", PhysicalHash: "gone", Size: 1, UpdatedAt: now,
		TrashedAt: &now, TrashID: "trash-missing", TrashRoot: true,
	}}}
	if _, err = BulkImportTreeV4(ctx, store, snapshot); err == nil {
		t.Fatal("import unexpectedly completed without readable trash manifest")
	}
	if _, found, getErr := backend.Get(ctx, store.prefix+"/v4/import/completed.json"); getErr != nil || found {
		t.Fatalf("completion found=%t err=%v", found, getErr)
	}
}

func TestTreeV4TrashRestoreAndHardDeleteAll(t *testing.T) {
	ctx := context.Background()
	store := newTestTreeV4(t, TreeV4Options{ShardCount: 8})
	for _, path := range []string{"one.txt", "two.txt"} {
		if _, _, err := store.ReplaceFileConditional(ctx, path, "object-"+path, 1, nil, true); err != nil {
			t.Fatal(err)
		}
	}
	firstNode, found, err := store.v4.resolve(ctx, "one.txt")
	if err != nil || !found {
		t.Fatalf("resolve first found=%t err=%v", found, err)
	}
	if _, err = store.TrashPaths(ctx, []TrashPath{{Path: "one.txt", TrashID: "trash-one"}}); err != nil {
		t.Fatal(err)
	}
	if records, listErr := store.ListTrashRecords(ctx, []string{"trash-one"}); listErr != nil || len(records) != 1 {
		t.Fatalf("trash records=%+v err=%v", records, listErr)
	}
	if _, err = store.RestoreTrash(ctx, []string{"trash-one"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err = store.Find(ctx, "one.txt"); err != nil || !found {
		t.Fatalf("restored found=%t err=%v", found, err)
	}
	if _, err = store.TrashPaths(ctx, []TrashPath{{Path: "one.txt", TrashID: "trash-one-final"}, {Path: "two.txt", TrashID: "trash-two-final"}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTrash(ctx, nil)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	deleted, err := store.DeleteTrash(ctx, nil)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if roots, listErr := store.ListTrash(ctx); listErr != nil || len(roots) != 0 {
		t.Fatalf("trash roots=%+v err=%v", roots, listErr)
	}
	if _, found, getErr := store.objects.Get(ctx, store.v4.nodeKey(firstNode.NodeID)); getErr != nil || found {
		t.Fatalf("deleted node found=%t err=%v", found, getErr)
	}
}
