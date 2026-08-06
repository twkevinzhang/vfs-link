package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newDerivedTestTree(t *testing.T) *TreeStore {
	t.Helper()
	backend, err := newLocalTreeBackend(filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	store := newTreeStore(backend, "_vfs-link-v4")
	t.Cleanup(store.Close)
	return store
}

func TestEmitDerivedDeltaIsImmutableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newDerivedTestTree(t)
	delta := MetadataStats{LogicalFiles: 1, LogicalBytes: 9, PhysicalObjects: 1, PhysicalBytes: 9}
	if err := store.emitDerivedDelta(ctx, "tx-1", delta, []string{"root", " parent ", "root"}); err != nil {
		t.Fatal(err)
	}
	if err := store.emitDerivedDelta(ctx, "tx-1", delta, []string{"parent", "root"}); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	keys, err := store.objects.List(ctx, store.treeDerivedDeltaPrefix())
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if err = store.emitDerivedDelta(ctx, "tx-1", MetadataStats{LogicalFiles: 2}, []string{"parent", "root"}); !errors.Is(err, ErrDerivedDeltaConflict) {
		t.Fatalf("conflicting token error=%v", err)
	}
	if strings.Contains(keys[0], "/stats.json") || !strings.Contains(keys[0], "/v4/derived/v1/") {
		t.Fatalf("delta escaped v4 derived namespace: %s", keys[0])
	}
}

func TestReduceDerivedDeltasReplaysWithoutDoubleApply(t *testing.T) {
	ctx := context.Background()
	store := newDerivedTestTree(t)
	delta := MetadataStats{LogicalFiles: 1, LogicalBytes: 12, PhysicalObjects: 1, PhysicalBytes: 12}
	if err := store.emitDerivedDelta(ctx, "tx-replay", delta, []string{"parent", "root"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	rebuilt := map[string]int{}
	options := TreeDerivedReduceOptions{Owner: "test", RebuildDirectory: func(_ context.Context, id string) error {
		mu.Lock()
		defer mu.Unlock()
		rebuilt[id]++
		return nil
	}}
	result, err := store.ReduceDerivedDeltas(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 1 || result.Pending != 0 || result.DirectoriesRebuilt != 2 {
		t.Fatalf("first result=%+v", result)
	}
	stats, found, err := store.DerivedMetadataStats(ctx)
	if err != nil || !found || !sameMetadataStats(stats, delta) {
		t.Fatalf("stats=%+v found=%v err=%v", stats, found, err)
	}
	result, err = store.ReduceDerivedDeltas(ctx, options)
	if err != nil || result.Applied != 0 {
		t.Fatalf("replay result=%+v err=%v", result, err)
	}
	stats, _, _ = store.DerivedMetadataStats(ctx)
	if !sameMetadataStats(stats, delta) {
		t.Fatalf("replay doubled stats: %+v", stats)
	}
	mu.Lock()
	defer mu.Unlock()
	if rebuilt["parent"] != 1 || rebuilt["root"] != 1 {
		t.Fatalf("rebuild counts=%v", rebuilt)
	}
}

type failDerivedDeleteBackend struct {
	treeBackend
	mu       sync.Mutex
	failOnce bool
}

type countDerivedStateBackend struct {
	treeBackend
	mu        sync.Mutex
	statePuts int
}

func (b *countDerivedStateBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	if strings.HasSuffix(key, "/v4/derived/v1/reducer/state.json") {
		b.mu.Lock()
		b.statePuts++
		b.mu.Unlock()
	}
	return b.treeBackend.Put(ctx, key, data, expected)
}

func TestDerivedReducerBatchesHotStateAndDirectoryRebuilds(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	backend := &countDerivedStateBackend{treeBackend: local}
	store := newTreeStore(backend, "_vfs-link-v4")
	t.Cleanup(store.Close)
	for index := 0; index < 24; index++ {
		if err = store.emitDerivedDelta(ctx, "batch-"+time.Unix(int64(index), 0).Format("150405"), MetadataStats{LogicalFiles: 1, LogicalBytes: 2}, []string{"parent", "root"}); err != nil {
			t.Fatal(err)
		}
	}
	rebuilds := map[string]int{}
	result, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{Owner: "batch", RebuildDirectory: func(_ context.Context, id string) error {
		rebuilds[id]++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	statePuts := backend.statePuts
	backend.mu.Unlock()
	if result.Applied != 24 || statePuts != 2 { // one batch checkpoint plus one token compaction
		t.Fatalf("result=%+v state puts=%d", result, statePuts)
	}
	if rebuilds["parent"] != 1 || rebuilds["root"] != 1 {
		t.Fatalf("rebuilds=%v", rebuilds)
	}
	stats, _, err := store.DerivedMetadataStats(ctx)
	if err != nil || stats.LogicalFiles != 24 || stats.LogicalBytes != 48 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func (b *failDerivedDeleteBackend) Delete(ctx context.Context, key string, expected *int64) error {
	b.mu.Lock()
	if b.failOnce && strings.Contains(key, "/v4/derived/v1/deltas/") {
		b.failOnce = false
		b.mu.Unlock()
		return errors.New("injected delete crash")
	}
	b.mu.Unlock()
	return b.treeBackend.Delete(ctx, key, expected)
}

func TestDerivedReducerRecoversCrashAfterCheckpoint(t *testing.T) {
	ctx := context.Background()
	local, err := newLocalTreeBackend(filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	backend := &failDerivedDeleteBackend{treeBackend: local, failOnce: true}
	store := newTreeStore(backend, "_vfs-link-v4")
	t.Cleanup(store.Close)
	delta := MetadataStats{LogicalDirs: 1}
	if err = store.emitDerivedDelta(ctx, "tx-crash", delta, nil); err != nil {
		t.Fatal(err)
	}
	first, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{Owner: "first"})
	if err == nil || first.Applied != 1 || first.Pending != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{Owner: "second"})
	if err != nil || second.Applied != 0 || second.Replayed != 1 || second.Pending != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	stats, found, err := store.DerivedMetadataStats(ctx)
	if err != nil || !found || stats.LogicalDirs != 1 {
		t.Fatalf("stats=%+v found=%v err=%v", stats, found, err)
	}
	state, _, _, err := store.loadDerivedReducerState(ctx)
	if err != nil || len(state.AppliedDeltaIDs) != 0 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestDerivedReducerLeaseCanRecoverAfterExpiry(t *testing.T) {
	ctx := context.Background()
	store := newDerivedTestTree(t)
	live := treeDerivedReducerLease{Version: treeDerivedSchemaVersion, Owner: "live", Until: time.Now().UTC().Add(time.Minute)}
	payload, _ := marshalTree(live)
	zero := int64(0)
	if _, err := store.objects.Put(ctx, store.treeDerivedLeaseKey(), payload, &zero); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{Owner: "other"}); !errors.Is(err, ErrDerivedReducerBusy) {
		t.Fatalf("live lease error=%v", err)
	}
	o, _, err := store.objects.Get(ctx, store.treeDerivedLeaseKey())
	if err != nil {
		t.Fatal(err)
	}
	expired := treeDerivedReducerLease{Version: treeDerivedSchemaVersion, Owner: "live", Until: time.Now().UTC().Add(-time.Second)}
	payload, _ = marshalTree(expired)
	if _, err = store.objects.Put(ctx, store.treeDerivedLeaseKey(), payload, &o.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{Owner: "recovery"}); err != nil {
		t.Fatalf("expired lease recovery: %v", err)
	}
}

func TestSeedDerivedMetadataIsConditionalAndRetryable(t *testing.T) {
	ctx := context.Background()
	store := newDerivedTestTree(t)
	stats := MetadataStats{LogicalFiles: 2, LogicalDirs: 1, LogicalBytes: 30, PhysicalObjects: 2, PhysicalBytes: 30}
	summaries := map[string]FolderSummary{"root": {Files: 2, Directories: 1, Bytes: 30}, "child": {Files: 1, Bytes: 10}}
	if err := store.SeedDerivedMetadata(ctx, stats, summaries); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedDerivedMetadata(ctx, stats, summaries); err != nil {
		t.Fatalf("identical seed retry: %v", err)
	}
	if err := store.SeedDerivedMetadata(ctx, MetadataStats{LogicalFiles: 99}, summaries); !errors.Is(err, ErrDerivedSeedConflict) {
		t.Fatalf("conflicting seed error=%v", err)
	}
	got, found, err := store.DerivedDirectorySummary(ctx, "root")
	if err != nil || !found || got != summaries["root"] {
		t.Fatalf("summary=%+v found=%v err=%v", got, found, err)
	}
}

func TestRunDerivedRecoveryReplaysBeforeReduction(t *testing.T) {
	ctx := context.Background()
	store := newDerivedTestTree(t)
	replayed := 0
	result, err := store.RunDerivedRecovery(ctx, TreeDerivedRecoveryOptions{
		TreeDerivedReduceOptions: TreeDerivedReduceOptions{Owner: "recovery"},
		ReplayCommittedTransactions: func(ctx context.Context) error {
			replayed++
			return store.emitDerivedDelta(ctx, "committed-tx", MetadataStats{LogicalFiles: 1}, nil)
		},
	})
	if err != nil || replayed != 1 || result.Applied != 1 {
		t.Fatalf("result=%+v replayed=%d err=%v", result, replayed, err)
	}
}

func TestV4DerivedReducerPublishesNestedStatsAndSummaries(t *testing.T) {
	ctx := context.Background()
	storeRaw, err := NewTreeLocalV4(filepath.Join(t.TempDir(), "metadata"), "_vfs-link-v4", TreeV4Options{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	store := storeRaw.(*TreeStore)
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertDirectory(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertDirectory(ctx, "docs/nested"); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertFile(ctx, "docs/nested/a.txt", "object-a", 7); err != nil {
		t.Fatal(err)
	}
	store.v4.waitFinalizers()
	result, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{Owner: "integration", RebuildDirectories: store.rebuildV4DirectorySummaries})
	if err != nil || result.Applied != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	stats, err := store.MetadataStats(ctx)
	if err != nil || stats.LogicalFiles != 1 || stats.LogicalDirs != 2 || stats.LogicalBytes != 7 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	root, err := store.ListDirectChildren(ctx, "", DirectChildrenOptions{})
	if err != nil || root.FolderSummary != (FolderSummary{Files: 1, Directories: 2, Bytes: 7}) {
		t.Fatalf("root=%+v err=%v", root, err)
	}
	docs, err := store.ListDirectChildren(ctx, "docs", DirectChildrenOptions{})
	if err != nil || docs.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 7}) {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
}
