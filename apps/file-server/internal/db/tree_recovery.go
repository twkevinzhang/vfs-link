package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type treeDerivedLeaseHandle struct {
	store      *TreeStore
	owner      string
	generation int64
	ttl        time.Duration
}

func (s *TreeStore) acquireDerivedReducerLease(ctx context.Context, owner string, ttl time.Duration) (*treeDerivedLeaseHandle, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = uuid.NewString()
	}
	if ttl <= 0 {
		ttl = defaultDerivedLeaseTTL
	}
	key := s.treeDerivedLeaseKey()
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		o, ok, err := s.objects.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if ok {
			var current treeDerivedReducerLease
			if err = json.Unmarshal(o.Data, &current); err != nil {
				return nil, err
			}
			if current.Until.After(time.Now().UTC()) && current.Owner != owner {
				return nil, ErrDerivedReducerBusy
			}
		}
		lease := treeDerivedReducerLease{Version: treeDerivedSchemaVersion, Owner: owner, Until: time.Now().UTC().Add(ttl)}
		payload, _ := marshalTree(lease)
		generation := o.Generation
		if !ok {
			generation = 0
		}
		newGeneration, putErr := s.objects.Put(ctx, key, payload, &generation)
		if putErr == nil {
			return &treeDerivedLeaseHandle{store: s, owner: owner, generation: newGeneration, ttl: ttl}, nil
		}
		if !errorsIsConflict(putErr) {
			return nil, putErr
		}
	}
	return nil, ErrMetadataConflict
}

func (h *treeDerivedLeaseHandle) renew(ctx context.Context) error {
	key := h.store.treeDerivedLeaseKey()
	o, ok, err := h.store.objects.Get(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("derived metadata reducer lease lost")
	}
	var current treeDerivedReducerLease
	if err = json.Unmarshal(o.Data, &current); err != nil {
		return err
	}
	if current.Owner != h.owner {
		return fmt.Errorf("derived metadata reducer lease lost")
	}
	current.Until = time.Now().UTC().Add(h.ttl)
	payload, _ := marshalTree(current)
	newGeneration, err := h.store.objects.Put(ctx, key, payload, &o.Generation)
	if err == nil {
		h.generation = newGeneration
	}
	return err
}

func (h *treeDerivedLeaseHandle) release() {
	_ = h.store.objects.Delete(context.Background(), h.store.treeDerivedLeaseKey(), &h.generation)
}

func (s *TreeStore) compactDerivedTokens(ctx context.Context) (int, error) {
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		state, generation, exists, err := s.loadDerivedReducerState(ctx)
		if err != nil || !exists || len(state.AppliedDeltaIDs) == 0 {
			return 0, err
		}
		kept := make([]string, 0, len(state.AppliedDeltaIDs))
		for _, token := range state.AppliedDeltaIDs {
			_, exists, getErr := s.objects.Get(ctx, s.treeDerivedDeltaKey(token))
			if getErr != nil {
				return 0, getErr
			}
			if exists {
				kept = append(kept, token)
			}
		}
		removed := len(state.AppliedDeltaIDs) - len(kept)
		if removed == 0 {
			return 0, nil
		}
		state.AppliedDeltaIDs = kept
		state.UpdatedAt = time.Now().UTC()
		payload, marshalErr := marshalTree(state)
		if marshalErr != nil {
			return 0, marshalErr
		}
		if _, err = s.objects.Put(ctx, s.treeDerivedStateKey(), payload, &generation); err == nil {
			return removed, nil
		} else if !errorsIsConflict(err) {
			return 0, err
		}
	}
	return 0, ErrMetadataConflict
}

// ReduceDerivedDeltas performs one bounded recovery/reducer pass. Deltas are
// checkpointed in the same CAS write that changes stats, then deleted. A crash
// before deletion replays the token without applying it twice; a crash after
// deletion is repaired by token compaction and snapshot publication.
func (s *TreeStore) ReduceDerivedDeltas(ctx context.Context, options TreeDerivedReduceOptions) (TreeDerivedReduceResult, error) {
	var result TreeDerivedReduceResult
	lease, err := s.acquireDerivedReducerLease(ctx, options.Owner, options.LeaseTTL)
	if err != nil {
		return result, err
	}
	defer lease.release()
	keys, err := s.objects.List(ctx, s.treeDerivedDeltaPrefix())
	if err != nil {
		return result, err
	}
	sort.Strings(keys)
	result.Discovered = len(keys)
	var passErrors []error
	type pendingDerivedObject struct {
		key        string
		generation int64
		delta      TreeDerivedDelta
	}
	objects := make([]pendingDerivedObject, 0, len(keys))
	for _, key := range keys {
		if err = lease.renew(ctx); err != nil {
			return result, err
		}
		o, ok, getErr := s.objects.Get(ctx, key)
		if getErr != nil {
			passErrors = append(passErrors, getErr)
			continue
		}
		if !ok {
			continue
		}
		var delta TreeDerivedDelta
		if getErr = json.Unmarshal(o.Data, &delta); getErr != nil {
			passErrors = append(passErrors, fmt.Errorf("decode derived metadata delta %q: %w", key, getErr))
			continue
		}
		if delta.Version != treeDerivedSchemaVersion || strings.TrimSpace(delta.TransactionToken) == "" {
			passErrors = append(passErrors, fmt.Errorf("invalid derived metadata delta %q", key))
			continue
		}
		objects = append(objects, pendingDerivedObject{key: key, generation: o.Generation, delta: delta})
	}
	deltas := make([]TreeDerivedDelta, 0, len(objects))
	for _, object := range objects {
		deltas = append(deltas, object.delta)
	}
	applied, rebuilt, applyErr := s.applyDerivedDeltaBatch(ctx, deltas, options.RebuildDirectory, options.RebuildDirectories)
	result.DirectoriesRebuilt += rebuilt
	if applyErr != nil {
		passErrors = append(passErrors, applyErr)
	} else {
		for _, object := range objects {
			if applied[object.delta.TransactionToken] {
				result.Applied++
			} else {
				result.Replayed++
			}
			if markErr := MarkTreeV4DerivedApplied(ctx, s, object.delta.TransactionToken); markErr != nil {
				passErrors = append(passErrors, markErr)
				continue
			}
			if deleteErr := s.objects.Delete(ctx, object.key, &object.generation); deleteErr != nil {
				passErrors = append(passErrors, deleteErr)
			}
		}
	}
	if compacted, compactErr := s.compactDerivedTokens(ctx); compactErr != nil {
		passErrors = append(passErrors, compactErr)
	} else {
		result.CompactedTokens = compacted
	}
	state, _, _, stateErr := s.loadDerivedReducerState(ctx)
	if stateErr != nil {
		passErrors = append(passErrors, stateErr)
	} else if publishErr := s.publishDerivedStats(ctx, state); publishErr != nil {
		passErrors = append(passErrors, publishErr)
	}
	pending, listErr := s.objects.List(ctx, s.treeDerivedDeltaPrefix())
	if listErr != nil {
		passErrors = append(passErrors, listErr)
	} else {
		result.Pending = len(pending)
	}
	return result, errors.Join(passErrors...)
}

type TreeDerivedRecoveryOptions struct {
	TreeDerivedReduceOptions
	// ReplayCommittedTransactions scans committed v4 manifests and calls
	// EmitDerivedDelta for transactions whose post-commit emission was lost.
	// It must be safe to run repeatedly.
	ReplayCommittedTransactions func(context.Context) error
}

// RunDerivedRecovery is the janitor entry point. Manifest replay happens
// before reduction, making a crash between namespace commit and delta emission
// recoverable without coupling the namespace transaction to derived metadata.
func (s *TreeStore) RunDerivedRecovery(ctx context.Context, options TreeDerivedRecoveryOptions) (TreeDerivedReduceResult, error) {
	var promotionErr error
	if s.v4 != nil {
		_, promotionErr = s.v4.recoverTransactions(ctx)
	}
	var replayErr error
	if options.ReplayCommittedTransactions != nil {
		replayErr = options.ReplayCommittedTransactions(ctx)
	}
	result, reduceErr := s.ReduceDerivedDeltas(ctx, options.TreeDerivedReduceOptions)
	return result, errors.Join(promotionErr, replayErr, reduceErr)
}

type TreeDerivedReducerLoopOptions struct {
	TreeDerivedRecoveryOptions
	// Interval controls the eventual-consistency window and must stay within
	// the production contract of one to five seconds.
	Interval time.Duration
	OnPass   func(TreeDerivedReduceResult)
	OnError  func(error)
}

// RunDerivedReducerLoop runs an immediate recovery pass and then continues at
// the configured one-to-five-second cadence until ctx is cancelled. Transient
// pass failures are reported and retried; they do not kill the janitor.
func (s *TreeStore) RunDerivedReducerLoop(ctx context.Context, options TreeDerivedReducerLoopOptions) error {
	if options.Interval < time.Second || options.Interval > 5*time.Second {
		return fmt.Errorf("derived metadata reducer interval must be between 1s and 5s")
	}
	run := func() {
		result, err := s.RunDerivedRecovery(ctx, options.TreeDerivedRecoveryOptions)
		if err != nil {
			if errors.Is(err, ErrDerivedReducerBusy) {
				return
			}
			if !errors.Is(err, context.Canceled) && options.OnError != nil {
				options.OnError(err)
			}
			return
		}
		if options.OnPass != nil {
			options.OnPass(result)
		}
	}
	run()
	ticker := time.NewTicker(options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			run()
		}
	}
}

// RunTreeDerivedReducerLoop is convenient for file-server wiring that owns a
// Store interface. Non-tree and v3 stores are intentionally no-ops.
func RunTreeDerivedReducerLoop(ctx context.Context, store Store, options TreeDerivedReducerLoopOptions) error {
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return nil
	}
	if options.RebuildDirectory == nil && options.RebuildDirectories == nil {
		options.RebuildDirectories = tree.rebuildV4DirectorySummaries
	}
	return tree.RunDerivedReducerLoop(ctx, options)
}
