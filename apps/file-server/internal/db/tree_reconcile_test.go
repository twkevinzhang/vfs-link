package db

import (
	"context"
	"testing"
)

func TestReconcileTreeV4DerivedStatsRepairsQuiescentDrift(t *testing.T) {
	ctx := context.Background()
	store := newTestTreeV4(t, TreeV4Options{ShardCount: 8})
	if err := store.UpsertDirectory(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReplaceFileConditional(ctx, "docs/a.txt", "object-a", 7, nil, true); err != nil {
		t.Fatal(err)
	}
	store.v4.waitFinalizers()
	if _, err := store.ReduceDerivedDeltas(ctx, TreeDerivedReduceOptions{RebuildDirectories: store.rebuildV4DirectorySummaries}); err != nil {
		t.Fatal(err)
	}
	state, generation, found, err := store.loadDerivedReducerState(ctx)
	if err != nil || !found {
		t.Fatalf("state found=%t err=%v", found, err)
	}
	state.Stats = MetadataStats{LogicalFiles: -10, LogicalDirs: -20, LogicalBytes: -30}
	payload, _ := marshalTree(state)
	if _, err = store.objects.Put(ctx, store.treeDerivedStateKey(), payload, &generation); err != nil {
		t.Fatal(err)
	}
	if err = store.publishDerivedStats(ctx, state); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileTreeV4DerivedStats(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	want := MetadataStats{LogicalFiles: 1, LogicalDirs: 1, LogicalBytes: 7, PhysicalObjects: 1, PhysicalBytes: 7}
	if result.Records != 2 || !sameMetadataStats(result.After, want) {
		t.Fatalf("result=%+v", result)
	}
	stats, found, err := store.DerivedMetadataStats(ctx)
	if err != nil || !found || !sameMetadataStats(stats, want) {
		t.Fatalf("stats=%+v found=%t err=%v", stats, found, err)
	}
}
