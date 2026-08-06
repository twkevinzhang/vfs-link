package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const derivedTokenRetention = 5 * time.Minute

type treeDerivedLeaseHandle struct {
	store      *TreeStore
	owner      string
	mu         sync.Mutex
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
	h.mu.Lock()
	defer h.mu.Unlock()
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
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.store.objects.Delete(context.Background(), h.store.treeDerivedLeaseKey(), &h.generation)
}

func (s *TreeStore) compactDerivedTokens(ctx context.Context, existingDeltaKeys []string) (int, error) {
	existing := make(map[string]struct{}, len(existingDeltaKeys))
	for _, key := range existingDeltaKeys {
		existing[key] = struct{}{}
	}
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		state, generation, exists, err := s.loadDerivedReducerState(ctx)
		if err != nil || !exists || len(state.AppliedDeltaIDs) == 0 {
			return 0, err
		}
		now := time.Now().UTC()
		if state.AppliedDeltaTimes == nil {
			state.AppliedDeltaTimes = make(map[string]time.Time)
		}
		kept := make([]string, 0, len(state.AppliedDeltaIDs))
		keptTimes := make(map[string]time.Time, len(state.AppliedDeltaIDs))
		for _, token := range state.AppliedDeltaIDs {
			appliedAt, timestamped := state.AppliedDeltaTimes[token]
			if !timestamped {
				// States written before token retention was introduced need one full
				// grace window before they can be compacted safely.
				appliedAt = now
			}
			_, deltaStillExists := existing[s.treeDerivedDeltaKey(token)]
			if deltaStillExists || now.Sub(appliedAt) < derivedTokenRetention {
				kept = append(kept, token)
				keptTimes[token] = appliedAt
			}
		}
		removed := len(state.AppliedDeltaIDs) - len(kept)
		metadataChanged := len(state.AppliedDeltaTimes) != len(keptTimes)
		if !metadataChanged {
			for token, appliedAt := range keptTimes {
				if current, found := state.AppliedDeltaTimes[token]; !found || !current.Equal(appliedAt) {
					metadataChanged = true
					break
				}
			}
		}
		if removed == 0 && !metadataChanged {
			return 0, nil
		}
		state.AppliedDeltaIDs = kept
		state.AppliedDeltaTimes = keptTimes
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
	workCtx, cancelWork := context.WithCancel(ctx)
	keepAliveDone := make(chan error, 1)
	go func() {
		period := lease.ttl / 3
		if period < 10*time.Millisecond {
			period = 10 * time.Millisecond
		}
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				keepAliveDone <- nil
				return
			case <-ticker.C:
				if renewErr := lease.renew(workCtx); renewErr != nil {
					cancelWork()
					keepAliveDone <- renewErr
					return
				}
			}
		}
	}()
	defer func() {
		cancelWork()
		<-keepAliveDone
		lease.release()
	}()
	keys, err := s.objects.List(workCtx, s.treeDerivedDeltaPrefix())
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
	objectsByKey := make([]pendingDerivedObject, len(keys))
	readErrors := make([]error, len(keys))
	tasks := make([]func(context.Context) error, 0, len(keys))
	for index, key := range keys {
		index, key := index, key
		tasks = append(tasks, func(taskCtx context.Context) error {
			o, ok, getErr := s.objects.Get(taskCtx, key)
			if getErr != nil {
				readErrors[index] = getErr
				return nil
			}
			if !ok {
				return nil
			}
			var delta TreeDerivedDelta
			if getErr = json.Unmarshal(o.Data, &delta); getErr != nil {
				readErrors[index] = fmt.Errorf("decode derived metadata delta %q: %w", key, getErr)
				return nil
			}
			if delta.Version != treeDerivedSchemaVersion || strings.TrimSpace(delta.TransactionToken) == "" {
				readErrors[index] = fmt.Errorf("invalid derived metadata delta %q", key)
				return nil
			}
			objectsByKey[index] = pendingDerivedObject{key: key, generation: o.Generation, delta: delta}
			return nil
		})
	}
	if err = runTreeImportTasks(workCtx, 32, tasks); err != nil {
		return result, err
	}
	objects := make([]pendingDerivedObject, 0, len(keys))
	for index, object := range objectsByKey {
		if readErrors[index] != nil {
			passErrors = append(passErrors, readErrors[index])
		} else if object.key != "" {
			objects = append(objects, object)
		}
	}
	deltas := make([]TreeDerivedDelta, 0, len(objects))
	for _, object := range objects {
		deltas = append(deltas, object.delta)
	}
	applied, rebuilt, applyErr := s.applyDerivedDeltaBatch(workCtx, deltas, options.RebuildDirectory, options.RebuildDirectories)
	result.DirectoriesRebuilt += rebuilt
	if applyErr != nil {
		passErrors = append(passErrors, applyErr)
	} else {
		var finalizeMu sync.Mutex
		finalizeTasks := make([]func(context.Context) error, 0, len(objects))
		for _, object := range objects {
			object := object
			finalizeTasks = append(finalizeTasks, func(taskCtx context.Context) error {
				markErr := MarkTreeV4DerivedApplied(taskCtx, s, object.delta.TransactionToken)
				if markErr == nil {
					markErr = s.objects.Delete(taskCtx, object.key, &object.generation)
				}
				finalizeMu.Lock()
				defer finalizeMu.Unlock()
				if applied[object.delta.TransactionToken] {
					result.Applied++
				} else {
					result.Replayed++
				}
				if markErr != nil {
					passErrors = append(passErrors, markErr)
				}
				return nil
			})
		}
		if finalizeErr := runTreeImportTasks(workCtx, 32, finalizeTasks); finalizeErr != nil {
			passErrors = append(passErrors, finalizeErr)
		}
	}
	pending, listErr := s.objects.List(workCtx, s.treeDerivedDeltaPrefix())
	if listErr != nil {
		passErrors = append(passErrors, listErr)
	} else {
		result.Pending = len(pending)
	}
	if compacted, compactErr := s.compactDerivedTokens(workCtx, pending); compactErr != nil {
		passErrors = append(passErrors, compactErr)
	} else {
		result.CompactedTokens = compacted
	}
	state, _, _, stateErr := s.loadDerivedReducerState(workCtx)
	if stateErr != nil {
		passErrors = append(passErrors, stateErr)
	} else if publishErr := s.publishDerivedStats(workCtx, state); publishErr != nil {
		passErrors = append(passErrors, publishErr)
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
	// Drain already-emitted deltas first. This bounds the active transaction set
	// before recovery scans it and prevents a large healthy reducer backlog from
	// turning startup recovery into a serial manifest replay.
	result, reduceErr := s.ReduceDerivedDeltas(ctx, options.TreeDerivedReduceOptions)
	if reduceErr != nil {
		return result, reduceErr
	}
	var promotionErr error
	if s.v4 != nil {
		_, promotionErr = s.v4.recoverTransactions(ctx)
	}
	var replayErr error
	if options.ReplayCommittedTransactions != nil {
		replayErr = options.ReplayCommittedTransactions(ctx)
	}
	if promotionErr != nil || replayErr != nil {
		return result, errors.Join(promotionErr, replayErr)
	}
	// Recovery can recreate a delta whose post-commit emission was interrupted.
	// Apply that bounded tail in the same loop iteration.
	tail, tailErr := s.ReduceDerivedDeltas(ctx, options.TreeDerivedReduceOptions)
	result.Discovered += tail.Discovered
	result.Applied += tail.Applied
	result.Replayed += tail.Replayed
	result.DirectoriesRebuilt += tail.DirectoriesRebuilt
	result.CompactedTokens += tail.CompactedTokens
	result.Pending = tail.Pending
	return result, tailErr
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
