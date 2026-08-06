package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TreeDerivedStatsReconcileResult records a maintenance-only repair of the
// global v4 stats snapshot from the strongly consistent namespace.
type TreeDerivedStatsReconcileResult struct {
	Records int
	Before  MetadataStats
	After   MetadataStats
}

// ReconcileTreeV4DerivedStats replaces only the global stats reducer state.
// It requires a quiescent v4 namespace and verifies the independently rebuilt
// root directory summary before writing, so it cannot hide namespace loss.
func ReconcileTreeV4DerivedStats(ctx context.Context, store Store) (TreeDerivedStatsReconcileResult, error) {
	var result TreeDerivedStatsReconcileResult
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return result, fmt.Errorf("derived stats reconciliation requires a v4 tree store")
	}
	tree.v4.waitFinalizers()
	lease, err := tree.acquireDerivedReducerLease(ctx, "reconcile-"+uuid.NewString(), 30*time.Second)
	if err != nil {
		return result, err
	}
	workCtx, cancel := context.WithCancel(ctx)
	keepAliveDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				keepAliveDone <- nil
				return
			case <-ticker.C:
				if renewErr := lease.renew(workCtx); renewErr != nil {
					cancel()
					keepAliveDone <- renewErr
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-keepAliveDone
		lease.release()
	}()
	assertQuiescent := func() error {
		deltas, listErr := tree.objects.List(workCtx, tree.treeDerivedDeltaPrefix())
		if listErr != nil {
			return listErr
		}
		active, listErr := tree.objects.List(workCtx, tree.prefix+"/v4/transactions/active/")
		if listErr != nil {
			return listErr
		}
		if len(deltas) != 0 || len(active) != 0 {
			return fmt.Errorf("v4 namespace is not quiescent: deltas=%d activeTransactions=%d", len(deltas), len(active))
		}
		return nil
	}
	if err = assertQuiescent(); err != nil {
		return result, err
	}
	records, err := tree.ListAll(workCtx)
	if err != nil {
		return result, err
	}
	result.Records = len(records)
	physical := make(map[string]int64)
	for _, record := range records {
		if record.IsDirectory {
			result.After.LogicalDirs++
			continue
		}
		result.After.LogicalFiles++
		result.After.LogicalBytes += record.Size
		if record.PhysicalHash != "" {
			physical[record.PhysicalHash] = record.Size
		}
	}
	for _, size := range physical {
		result.After.PhysicalObjects++
		result.After.PhysicalBytes += size
	}
	rootSummary, found, err := tree.DerivedDirectorySummary(workCtx, "root")
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("root derived directory summary is missing")
		}
		return result, err
	}
	wantRoot := FolderSummary{Files: result.After.LogicalFiles, Directories: result.After.LogicalDirs, Bytes: result.After.LogicalBytes}
	if rootSummary != wantRoot {
		return result, fmt.Errorf("root summary does not match namespace: got %+v want %+v", rootSummary, wantRoot)
	}
	if err = assertQuiescent(); err != nil {
		return result, err
	}
	state, generation, exists, err := tree.loadDerivedReducerState(workCtx)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("derived reducer state is missing")
		}
		return result, err
	}
	result.Before = state.Stats
	state.Stats = result.After
	state.AppliedDeltaIDs = nil
	state.AppliedDeltaTimes = nil
	state.UpdatedAt = time.Now().UTC()
	payload, err := marshalTree(state)
	if err != nil {
		return result, err
	}
	if _, err = tree.objects.Put(workCtx, tree.treeDerivedStateKey(), payload, &generation); err != nil {
		return result, err
	}
	if err = tree.publishDerivedStats(workCtx, state); err != nil {
		return result, err
	}
	return result, nil
}
