package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	treeDerivedSchemaVersion = 1
	defaultDerivedLeaseTTL   = 15 * time.Second
)

var (
	ErrDerivedDeltaConflict = errors.New("derived metadata delta token has different content")
	ErrDerivedReducerBusy   = errors.New("derived metadata reducer is already running")
	ErrDerivedSeedConflict  = errors.New("derived metadata seed differs from existing state")
)

// TreeDerivedDelta is an immutable, transaction-addressed description of
// eventually consistent metadata. The transaction token is the idempotency
// key: replaying a committed transaction is safe, while reusing its token for
// different content is rejected.
type TreeDerivedDelta struct {
	Version              int           `json:"version"`
	TransactionToken     string        `json:"transactionToken"`
	StatsDelta           MetadataStats `json:"statsDelta,omitempty"`
	AncestorDirectoryIDs []string      `json:"ancestorDirectoryIds,omitempty"`
	CreatedAt            time.Time     `json:"createdAt"`
}

type treeDerivedReducerState struct {
	Version           int                  `json:"version"`
	Stats             MetadataStats        `json:"stats"`
	AppliedDeltaIDs   []string             `json:"appliedDeltaIds,omitempty"`
	AppliedDeltaTimes map[string]time.Time `json:"appliedDeltaTimes,omitempty"`
	UpdatedAt         time.Time            `json:"updatedAt"`
}

type treeDerivedStatsSnapshot struct {
	Version   int           `json:"version"`
	Stats     MetadataStats `json:"stats"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

type treeDerivedDirectorySnapshot struct {
	Version     int           `json:"version"`
	DirectoryID string        `json:"directoryId"`
	Summary     FolderSummary `json:"summary"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

type treeDerivedReducerLease struct {
	Version int       `json:"version"`
	Owner   string    `json:"owner"`
	Until   time.Time `json:"until"`
}

// TreeDerivedReduceOptions configures one reducer pass. RebuildDirectory must
// be idempotent and publish an absolute summary. It can run again after a
// crash before the delta is checkpointed.
type TreeDerivedReduceOptions struct {
	Owner    string
	LeaseTTL time.Duration
	// RetainLease keeps the lease object until its TTL expires. Long-running
	// reducer loops use a stable owner and renew once per interval, avoiding a
	// create/delete burst against GCS's per-object mutation limit.
	RetainLease        bool
	RebuildDirectory   func(context.Context, string) error
	RebuildDirectories func(context.Context, []string) error
}

type TreeDerivedReduceResult struct {
	Discovered         int
	Applied            int
	Replayed           int
	DirectoriesRebuilt int
	CompactedTokens    int
	Pending            int
}

func (s *TreeStore) treeDerivedPrefix() string {
	return s.prefix + "/v4/derived/v1"
}

func (s *TreeStore) treeDerivedDeltaPrefix() string {
	return s.treeDerivedPrefix() + "/deltas/"
}

func treeDerivedTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *TreeStore) treeDerivedDeltaKey(token string) string {
	hash := treeDerivedTokenHash(token)
	return s.treeDerivedDeltaPrefix() + hash[:2] + "/" + hash + ".json"
}

func (s *TreeStore) treeDerivedStateKey() string {
	return s.treeDerivedPrefix() + "/reducer/state.json"
}

func (s *TreeStore) treeDerivedLeaseKey() string {
	return s.treeDerivedPrefix() + "/leases/reducer.json"
}

func (s *TreeStore) treeDerivedStatsKey() string {
	return s.treeDerivedPrefix() + "/stats/current.json"
}

func (s *TreeStore) treeDerivedDirectoryKey(directoryID string) string {
	hash := treeDerivedTokenHash(directoryID)
	return s.treeDerivedPrefix() + "/directories/" + hash[:2] + "/" + hash + ".json"
}

func normalizeDerivedDirectoryIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func normalizeDerivedStatsDelta(delta MetadataStats) MetadataStats {
	delta.AppliedOperationIDs = nil
	return delta
}

func sameDerivedDelta(a, b TreeDerivedDelta) bool {
	return a.Version == b.Version &&
		a.TransactionToken == b.TransactionToken &&
		a.StatsDelta.LogicalFiles == b.StatsDelta.LogicalFiles &&
		a.StatsDelta.LogicalDirs == b.StatsDelta.LogicalDirs &&
		a.StatsDelta.LogicalBytes == b.StatsDelta.LogicalBytes &&
		a.StatsDelta.PhysicalObjects == b.StatsDelta.PhysicalObjects &&
		a.StatsDelta.PhysicalBytes == b.StatsDelta.PhysicalBytes &&
		slices.Equal(a.AncestorDirectoryIDs, b.AncestorDirectoryIDs)
}

// emitDerivedDelta persists a v4 derived-metadata event after the namespace
// transaction has committed. Failure is retryable with the same transaction
// token and must never roll back the already committed namespace mutation.
func (s *TreeStore) emitDerivedDelta(ctx context.Context, transactionToken string, statsDelta MetadataStats, ancestorDirectoryIDs []string) error {
	transactionToken = strings.TrimSpace(transactionToken)
	if transactionToken == "" {
		return fmt.Errorf("derived metadata transaction token is required")
	}
	delta := TreeDerivedDelta{
		Version:              treeDerivedSchemaVersion,
		TransactionToken:     transactionToken,
		StatsDelta:           normalizeDerivedStatsDelta(statsDelta),
		AncestorDirectoryIDs: normalizeDerivedDirectoryIDs(ancestorDirectoryIDs),
		CreatedAt:            time.Now().UTC(),
	}
	key := s.treeDerivedDeltaKey(transactionToken)
	if existing, ok, err := s.objects.Get(ctx, key); err != nil {
		return err
	} else if ok {
		var current TreeDerivedDelta
		if err = json.Unmarshal(existing.Data, &current); err != nil {
			return fmt.Errorf("decode existing derived metadata delta %q: %w", transactionToken, err)
		}
		if sameDerivedDelta(current, delta) {
			return nil
		}
		return ErrDerivedDeltaConflict
	}
	payload, err := marshalTree(delta)
	if err != nil {
		return err
	}
	zero := int64(0)
	if _, err = s.objects.Put(ctx, key, payload, &zero); err == nil {
		return nil
	} else if !errorsIsConflict(err) {
		return err
	}
	// Another replay can win the conditional create. CreatedAt is deliberately
	// excluded from semantic equality because it is not part of the mutation.
	existing, ok, getErr := s.objects.Get(ctx, key)
	if getErr != nil {
		return getErr
	}
	if !ok {
		return err
	}
	var current TreeDerivedDelta
	if getErr = json.Unmarshal(existing.Data, &current); getErr != nil {
		return fmt.Errorf("decode concurrent derived metadata delta %q: %w", transactionToken, getErr)
	}
	if sameDerivedDelta(current, delta) {
		return nil
	}
	return ErrDerivedDeltaConflict
}

// EmitDerivedDelta is the public integration point for commit/recovery code
// outside the db package.
func (s *TreeStore) EmitDerivedDelta(ctx context.Context, transactionToken string, statsDelta MetadataStats, ancestorDirectoryIDs []string) error {
	return s.emitDerivedDelta(ctx, transactionToken, statsDelta, ancestorDirectoryIDs)
}

func addMetadataStats(dst *MetadataStats, delta MetadataStats) {
	dst.LogicalFiles += delta.LogicalFiles
	dst.LogicalDirs += delta.LogicalDirs
	dst.LogicalBytes += delta.LogicalBytes
	dst.PhysicalObjects += delta.PhysicalObjects
	dst.PhysicalBytes += delta.PhysicalBytes
}

func sameMetadataStats(a, b MetadataStats) bool {
	return a.LogicalFiles == b.LogicalFiles &&
		a.LogicalDirs == b.LogicalDirs &&
		a.LogicalBytes == b.LogicalBytes &&
		a.PhysicalObjects == b.PhysicalObjects &&
		a.PhysicalBytes == b.PhysicalBytes
}

func containsDerivedToken(ids []string, token string) bool {
	return slices.Contains(ids, token)
}

func (s *TreeStore) loadDerivedReducerState(ctx context.Context) (treeDerivedReducerState, int64, bool, error) {
	o, ok, err := s.objects.Get(ctx, s.treeDerivedStateKey())
	if err != nil || !ok {
		return treeDerivedReducerState{Version: treeDerivedSchemaVersion}, 0, ok, err
	}
	var state treeDerivedReducerState
	if err = json.Unmarshal(o.Data, &state); err != nil {
		return state, 0, true, err
	}
	return state, o.Generation, true, nil
}

func (s *TreeStore) applyDerivedDeltaBatch(ctx context.Context, deltas []TreeDerivedDelta, rebuild func(context.Context, string) error, rebuildBatch func(context.Context, []string) error) (map[string]bool, int, error) {
	applied := make(map[string]bool, len(deltas))
	rebuiltIDs := make(map[string]bool)
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		state, generation, exists, err := s.loadDerivedReducerState(ctx)
		if err != nil {
			return nil, len(rebuiltIDs), err
		}
		pending := make([]TreeDerivedDelta, 0, len(deltas))
		directoryIDs := map[string]bool{}
		for _, delta := range deltas {
			if containsDerivedToken(state.AppliedDeltaIDs, delta.TransactionToken) {
				continue
			}
			pending = append(pending, delta)
			for _, directoryID := range delta.AncestorDirectoryIDs {
				directoryIDs[directoryID] = true
			}
		}
		if len(pending) == 0 {
			return applied, len(rebuiltIDs), nil
		}
		if len(directoryIDs) > 0 && rebuild == nil && rebuildBatch == nil {
			return nil, len(rebuiltIDs), fmt.Errorf("derived metadata batch requires a directory rebuilder")
		}
		orderedDirectories := make([]string, 0, len(directoryIDs))
		for directoryID := range directoryIDs {
			orderedDirectories = append(orderedDirectories, directoryID)
		}
		sort.Strings(orderedDirectories)
		if rebuildBatch != nil {
			var unrepaired []string
			for _, directoryID := range orderedDirectories {
				if !rebuiltIDs[directoryID] {
					unrepaired = append(unrepaired, directoryID)
				}
			}
			if len(unrepaired) > 0 {
				if err = rebuildBatch(ctx, unrepaired); err != nil {
					return nil, len(rebuiltIDs), fmt.Errorf("rebuild derived directories: %w", err)
				}
				for _, directoryID := range unrepaired {
					rebuiltIDs[directoryID] = true
				}
			}
		} else {
			for _, directoryID := range orderedDirectories {
				if rebuiltIDs[directoryID] {
					continue
				}
				if err = rebuild(ctx, directoryID); err != nil {
					return nil, len(rebuiltIDs), fmt.Errorf("rebuild derived directory %q: %w", directoryID, err)
				}
				rebuiltIDs[directoryID] = true
			}
		}
		for _, delta := range pending {
			addMetadataStats(&state.Stats, delta.StatsDelta)
			state.AppliedDeltaIDs = append(state.AppliedDeltaIDs, delta.TransactionToken)
			if state.AppliedDeltaTimes == nil {
				state.AppliedDeltaTimes = make(map[string]time.Time)
			}
			state.AppliedDeltaTimes[delta.TransactionToken] = time.Now().UTC()
		}
		state.Version = treeDerivedSchemaVersion
		state.Stats.AppliedOperationIDs = nil
		sort.Strings(state.AppliedDeltaIDs)
		state.UpdatedAt = time.Now().UTC()
		payload, marshalErr := marshalTree(state)
		if marshalErr != nil {
			return nil, len(rebuiltIDs), marshalErr
		}
		if !exists {
			generation = 0
		}
		if _, err = s.objects.Put(ctx, s.treeDerivedStateKey(), payload, &generation); err == nil {
			for _, delta := range pending {
				applied[delta.TransactionToken] = true
			}
			return applied, len(rebuiltIDs), nil
		} else if !errorsIsConflict(err) {
			return nil, len(rebuiltIDs), err
		}
		// A reducer lease normally prevents this CAS conflict. If an ambiguous
		// response or a recovery race does occur, re-read the token before any
		// further work. Directory rebuilds publish absolute values and are safe
		// to repeat.
	}
	return nil, len(rebuiltIDs), ErrMetadataConflict
}

func (s *TreeStore) publishDerivedStats(ctx context.Context, state treeDerivedReducerState) error {
	snapshot := treeDerivedStatsSnapshot{Version: treeDerivedSchemaVersion, Stats: state.Stats, UpdatedAt: time.Now().UTC()}
	snapshot.Stats.AppliedOperationIDs = nil
	payload, err := marshalTree(snapshot)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		o, ok, getErr := s.objects.Get(ctx, s.treeDerivedStatsKey())
		if getErr != nil {
			return getErr
		}
		generation := o.Generation
		if !ok {
			generation = 0
		}
		if _, err = s.objects.Put(ctx, s.treeDerivedStatsKey(), payload, &generation); err == nil {
			return nil
		} else if !errorsIsConflict(err) {
			return err
		}
	}
	return ErrMetadataConflict
}

// DerivedMetadataStats reads the reducer's independently published v4 stats.
func (s *TreeStore) DerivedMetadataStats(ctx context.Context) (MetadataStats, bool, error) {
	o, ok, err := s.objects.Get(ctx, s.treeDerivedStatsKey())
	if err != nil || !ok {
		return MetadataStats{}, ok, err
	}
	var snapshot treeDerivedStatsSnapshot
	if err = json.Unmarshal(o.Data, &snapshot); err != nil {
		return MetadataStats{}, true, err
	}
	return snapshot.Stats, true, nil
}

// DerivedDirectorySummary reads a stable-directory-ID summary independently
// of namespace shards. Missing snapshots are represented as found=false so a
// newly created empty directory can safely render as zero while its delta is
// pending.
func (s *TreeStore) DerivedDirectorySummary(ctx context.Context, directoryID string) (FolderSummary, bool, error) {
	directoryID = strings.TrimSpace(directoryID)
	if directoryID == "" {
		return FolderSummary{}, false, fmt.Errorf("derived directory ID is required")
	}
	o, ok, err := s.objects.Get(ctx, s.treeDerivedDirectoryKey(directoryID))
	if err != nil || !ok {
		return FolderSummary{}, ok, err
	}
	var snapshot treeDerivedDirectorySnapshot
	if err = json.Unmarshal(o.Data, &snapshot); err != nil {
		return FolderSummary{}, true, err
	}
	if snapshot.DirectoryID != directoryID {
		return FolderSummary{}, true, fmt.Errorf("derived directory summary ID mismatch")
	}
	return snapshot.Summary, true, nil
}

// PublishDerivedDirectorySummary CAS-publishes an absolute summary. Repeating
// the same recovery work is safe, which is required when a reducer crashes
// between rebuilding directories and checkpointing its delta token.
func (s *TreeStore) PublishDerivedDirectorySummary(ctx context.Context, directoryID string, summary FolderSummary) error {
	directoryID = strings.TrimSpace(directoryID)
	if directoryID == "" {
		return fmt.Errorf("derived directory ID is required")
	}
	snapshot := treeDerivedDirectorySnapshot{Version: treeDerivedSchemaVersion, DirectoryID: directoryID, Summary: summary, UpdatedAt: time.Now().UTC()}
	payload, err := marshalTree(snapshot)
	if err != nil {
		return err
	}
	key := s.treeDerivedDirectoryKey(directoryID)
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		o, ok, getErr := s.objects.Get(ctx, key)
		if getErr != nil {
			return getErr
		}
		generation := o.Generation
		if !ok {
			generation = 0
		}
		if _, err = s.objects.Put(ctx, key, payload, &generation); err == nil {
			return nil
		} else if !errorsIsConflict(err) {
			return err
		}
	}
	return ErrMetadataConflict
}

func (s *TreeStore) readV4DirectoryEntries(ctx context.Context, directoryID string) ([]treeV4DirectoryEntry, error) {
	if s.v4 == nil {
		return nil, fmt.Errorf("v4 directory summary requires a v4 tree store")
	}
	prefix := s.prefix + "/v4/directories/" + encodeTreeSegment(directoryID) + "/shards/"
	keys, err := s.objects.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	shards := make([]treeV4DirectoryShard, len(keys))
	tasks := make([]func(context.Context) error, 0, len(keys))
	for index, key := range keys {
		index, key := index, key
		tasks = append(tasks, func(taskCtx context.Context) error {
			raw, _, found, readErr := s.v4.visibleEnvelope(taskCtx, key)
			if readErr != nil || !found {
				return readErr
			}
			return json.Unmarshal(raw, &shards[index])
		})
	}
	if err = runTreeImportTasks(ctx, 16, tasks); err != nil {
		return nil, err
	}
	var entries []treeV4DirectoryEntry
	for _, shard := range shards {
		for _, entry := range shard.Entries {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// rebuildV4DirectorySummaries computes a dirty batch bottom-up. Only dirty
// directories have their shards scanned; clean child subtrees use their last
// absolute snapshot. This makes each reducer pass proportional to the changed
// ancestor set instead of the complete namespace.
func (s *TreeStore) rebuildV4DirectorySummaries(ctx context.Context, directoryIDs []string) error {
	dirty := make(map[string]bool, len(directoryIDs))
	for _, id := range normalizeDerivedDirectoryIDs(directoryIDs) {
		dirty[id] = true
	}
	entries := make(map[string][]treeV4DirectoryEntry, len(dirty))
	for id := range dirty {
		value, err := s.readV4DirectoryEntries(ctx, id)
		if err != nil {
			return fmt.Errorf("read v4 directory %q: %w", id, err)
		}
		entries[id] = value
	}
	memo := make(map[string]FolderSummary, len(dirty))
	visiting := make(map[string]bool, len(dirty))
	var summarize func(string) (FolderSummary, error)
	summarize = func(id string) (FolderSummary, error) {
		if summary, ok := memo[id]; ok {
			return summary, nil
		}
		if visiting[id] {
			return FolderSummary{}, fmt.Errorf("cycle detected in v4 directory graph at %q", id)
		}
		visiting[id] = true
		defer delete(visiting, id)
		children, loaded := entries[id]
		if !loaded {
			var err error
			children, err = s.readV4DirectoryEntries(ctx, id)
			if err != nil {
				return FolderSummary{}, err
			}
		}
		var summary FolderSummary
		for _, entry := range children {
			if !entry.Node.IsDirectory {
				summary.Files++
				summary.Bytes += entry.Node.Size
				continue
			}
			summary.Directories++
			var childSummary FolderSummary
			var found bool
			var err error
			if dirty[entry.Node.NodeID] {
				childSummary, err = summarize(entry.Node.NodeID)
			} else {
				childSummary, found, err = s.DerivedDirectorySummary(ctx, entry.Node.NodeID)
				if err == nil && !found {
					// A new empty directory legitimately has no snapshot yet. If it
					// already has children, recursively establish its baseline.
					childSummary, err = summarize(entry.Node.NodeID)
				}
			}
			if err != nil {
				return FolderSummary{}, err
			}
			addFolderSummary(&summary, childSummary)
		}
		memo[id] = summary
		if err := s.PublishDerivedDirectorySummary(ctx, id, summary); err != nil {
			return FolderSummary{}, err
		}
		return summary, nil
	}
	for id := range dirty {
		if _, err := summarize(id); err != nil {
			return fmt.Errorf("summarize v4 directory %q: %w", id, err)
		}
	}
	return nil
}

// SeedDerivedMetadata installs the migration baseline without overwriting any
// existing reducer state or directory summary. A partially completed seed is
// safely retryable with identical values during the maintenance window.
func (s *TreeStore) SeedDerivedMetadata(ctx context.Context, stats MetadataStats, directorySummaries map[string]FolderSummary) error {
	stats = normalizeDerivedStatsDelta(stats)
	state := treeDerivedReducerState{Version: treeDerivedSchemaVersion, Stats: stats, UpdatedAt: time.Now().UTC()}
	statePayload, err := marshalTree(state)
	if err != nil {
		return err
	}
	zero := int64(0)
	if _, err = s.objects.Put(ctx, s.treeDerivedStateKey(), statePayload, &zero); err != nil {
		if !errorsIsConflict(err) {
			return err
		}
		existing, _, found, getErr := s.loadDerivedReducerState(ctx)
		if getErr != nil {
			return getErr
		}
		if !found || len(existing.AppliedDeltaIDs) != 0 || !sameMetadataStats(existing.Stats, stats) {
			return ErrDerivedSeedConflict
		}
	}
	ids := make([]string, 0, len(directorySummaries))
	for id := range directorySummaries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return fmt.Errorf("derived directory ID is required")
		}
		summary := directorySummaries[rawID]
		snapshot := treeDerivedDirectorySnapshot{Version: treeDerivedSchemaVersion, DirectoryID: id, Summary: summary, UpdatedAt: time.Now().UTC()}
		payload, marshalErr := marshalTree(snapshot)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = s.objects.Put(ctx, s.treeDerivedDirectoryKey(id), payload, &zero); err == nil {
			continue
		} else if !errorsIsConflict(err) {
			return err
		}
		existing, found, getErr := s.DerivedDirectorySummary(ctx, id)
		if getErr != nil {
			return getErr
		}
		if !found || existing != summary {
			return ErrDerivedSeedConflict
		}
	}
	snapshot := treeDerivedStatsSnapshot{Version: treeDerivedSchemaVersion, Stats: stats, UpdatedAt: time.Now().UTC()}
	payload, marshalErr := marshalTree(snapshot)
	if marshalErr != nil {
		return marshalErr
	}
	if _, err = s.objects.Put(ctx, s.treeDerivedStatsKey(), payload, &zero); err == nil {
		return nil
	} else if !errorsIsConflict(err) {
		return err
	}
	existingStats, found, getErr := s.DerivedMetadataStats(ctx)
	if getErr != nil {
		return getErr
	}
	if !found || !sameMetadataStats(existingStats, stats) {
		return ErrDerivedSeedConflict
	}
	return nil
}

// SeedTreeDerivedMetadata is the Store-interface migration integration point.
func SeedTreeDerivedMetadata(ctx context.Context, store Store, stats MetadataStats, directorySummaries map[string]FolderSummary) error {
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return fmt.Errorf("derived metadata seed requires a v4 tree store")
	}
	return tree.SeedDerivedMetadata(ctx, stats, directorySummaries)
}
