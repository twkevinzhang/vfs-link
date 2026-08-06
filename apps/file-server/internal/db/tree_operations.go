package db

import (
	"context"
	"encoding/json"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type OperationRecord struct {
	ID          string        `json:"id"`
	Type        string        `json:"type"`
	Status      string        `json:"status"`
	Paths       []string      `json:"paths"`
	Destination string        `json:"destination,omitempty"`
	Progress    int           `json:"progress"`
	Total       int           `json:"total"`
	Error       string        `json:"error,omitempty"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
	Result      []FileRecord  `json:"result,omitempty"`
	TrashItems  []TrashPath   `json:"trashItems,omitempty"`
	TrashIDs    []string      `json:"trashIds,omitempty"`
	Deleted     int64         `json:"deleted,omitempty"`
	LeaseOwner  string        `json:"leaseOwner,omitempty"`
	LeaseUntil  *time.Time    `json:"leaseUntil,omitempty"`
	StatsDelta  MetadataStats `json:"statsDelta,omitempty"`
}

type TreeOperationStore interface {
	CreateMoveOperation(context.Context, []string, string) (OperationRecord, error)
	CreateRenameOperation(context.Context, string, string) (OperationRecord, error)
	CreateTrashOperation(context.Context, []TrashPath) (OperationRecord, error)
	CreateRestoreOperation(context.Context, []string) (OperationRecord, error)
	CreateDeleteTrashOperation(context.Context, []string) (OperationRecord, error)
	GetOperation(context.Context, string) (OperationRecord, bool, error)
	ListRunnableOperations(context.Context) ([]OperationRecord, error)
	RunOperation(context.Context, string) (OperationRecord, error)
	RunDeleteTrashOperation(context.Context, string, func(context.Context, []string, func(int, int) error) (int64, error)) (OperationRecord, error)
}

const deleteTrashCheckpointInterval = 5 * time.Second

func (s *TreeStore) RunDeleteTrashOperation(ctx context.Context, id string, executor func(context.Context, []string, func(int, int) error) (int64, error)) (OperationRecord, error) {
	op, g, ok, e := s.loadOperation(ctx, id)
	if e != nil {
		return op, e
	}
	if !ok {
		return op, ErrNotFound
	}
	if op.Type != "delete-trash" {
		return op, fmt.Errorf("operation is not delete-trash")
	}
	if op.Status == "completed" {
		return op, nil
	}
	if op.Status == "running" && op.LeaseUntil != nil && op.LeaseUntil.After(time.Now()) {
		return op, fmt.Errorf("operation is already running")
	}
	owner := uuid.NewString()
	until := time.Now().UTC().Add(5 * time.Minute)
	op.Status = "running"
	op.LeaseOwner = owner
	op.LeaseUntil = &until
	if g, e = s.saveOperationCAS(ctx, op, g); e != nil {
		return op, e
	}
	lastCheckpoint := time.Now()
	checkpoint := func(done, total int) error {
		// Object deletion is idempotent, so progress is an optimization for
		// visibility and lease renewal rather than a correctness boundary. Avoid
		// rewriting the same GCS operation object for every file: that exceeds the
		// per-object mutation rate limit on even modest directory deletes. The
		// terminal CAS below persists the final progress.
		if done >= total || time.Since(lastCheckpoint) < deleteTrashCheckpointInterval {
			op.Progress = done
			op.Total = total
			return nil
		}
		current, cg, ok, e := s.loadOperation(ctx, id)
		if e != nil {
			return e
		}
		if !ok {
			return ErrNotFound
		}
		if current.LeaseOwner != owner {
			return fmt.Errorf("operation lease lost")
		}
		if done <= current.Progress {
			op = current
			lastCheckpoint = time.Now()
			return nil
		}
		current.Progress = done
		current.Total = total
		renew := time.Now().UTC().Add(5 * time.Minute)
		current.LeaseUntil = &renew
		ng, e := s.saveOperationCAS(ctx, current, cg)
		if e == nil {
			op = current
			g = ng
			lastCheckpoint = time.Now()
		}
		return e
	}
	deleted, runErr := executor(ctx, op.TrashIDs, checkpoint)
	current, cg, ok, e := s.loadOperation(ctx, id)
	if e != nil {
		return op, e
	}
	if !ok {
		return op, ErrNotFound
	}
	if current.LeaseOwner != owner {
		return current, fmt.Errorf("operation lease lost")
	}
	current.Deleted = deleted
	if op.Total > current.Total {
		current.Total = op.Total
	}
	if op.Progress > current.Progress {
		current.Progress = op.Progress
	}
	current.LeaseOwner = ""
	current.LeaseUntil = nil
	if runErr != nil {
		current.Status = "pending"
		current.Error = runErr.Error()
	} else {
		current.Status = "completed"
		current.Error = ""
		current.Progress = current.Total
	}
	_, e = s.saveOperationCAS(ctx, current, cg)
	if e != nil {
		return current, e
	}
	return current, runErr
}

func (s *TreeStore) createOperation(ctx context.Context, op OperationRecord) (OperationRecord, error) {
	op.ID = uuid.NewString()
	op.Status = "pending"
	op.CreatedAt = time.Now().UTC()
	e := s.saveOperation(ctx, op)
	return op, e
}

func (s *TreeStore) operationKey(id string) string {
	return s.prefix + "/operations/" + urlPathEscape(id) + ".json"
}
func urlPathEscape(v string) string { return strings.NewReplacer("%", "%25", "/", "%2F").Replace(v) }
func (s *TreeStore) saveOperation(ctx context.Context, op OperationRecord) error {
	op.UpdatedAt = time.Now().UTC()
	b, e := marshalTree(op)
	if e != nil {
		return e
	}
	_, e = s.objects.Put(ctx, s.operationKey(op.ID), b, nil)
	return e
}
func (s *TreeStore) loadOperation(ctx context.Context, id string) (OperationRecord, int64, bool, error) {
	o, ok, e := s.objects.Get(ctx, s.operationKey(id))
	if e != nil || !ok {
		return OperationRecord{}, 0, ok, e
	}
	var op OperationRecord
	e = json.Unmarshal(o.Data, &op)
	return op, o.Generation, true, e
}
func (s *TreeStore) saveOperationCAS(ctx context.Context, op OperationRecord, g int64) (int64, error) {
	op.UpdatedAt = time.Now().UTC()
	b, e := marshalTree(op)
	if e != nil {
		return 0, e
	}
	return s.objects.Put(ctx, s.operationKey(op.ID), b, &g)
}
func (s *TreeStore) CreateMoveOperation(ctx context.Context, paths []string, destination string) (OperationRecord, error) {
	roots, e := normalizeRoots(paths)
	if e != nil {
		return OperationRecord{}, e
	}
	if strings.HasPrefix(strings.TrimSpace(destination), "/") {
		return OperationRecord{}, fmt.Errorf("logical path must not start with a slash")
	}
	op := OperationRecord{ID: uuid.NewString(), Type: "move", Status: "pending", Paths: roots, Destination: cleanLogicPath(destination), CreatedAt: time.Now().UTC()}
	e = s.saveOperation(ctx, op)
	return op, e
}
func (s *TreeStore) CreateRenameOperation(ctx context.Context, logicPath, name string) (OperationRecord, error) {
	from, to, e := RenameTarget(logicPath, name)
	if e != nil {
		return OperationRecord{}, e
	}
	return s.createOperation(ctx, OperationRecord{Type: "rename", Paths: []string{from}, Destination: to})
}
func (s *TreeStore) CreateTrashOperation(ctx context.Context, items []TrashPath) (OperationRecord, error) {
	if len(items) == 0 {
		return OperationRecord{}, fmt.Errorf("at least one trash path is required")
	}
	return s.createOperation(ctx, OperationRecord{Type: "trash", TrashItems: items})
}
func (s *TreeStore) CreateRestoreOperation(ctx context.Context, ids []string) (OperationRecord, error) {
	if len(cleanTrashIDs(ids)) == 0 {
		return OperationRecord{}, fmt.Errorf("at least one trash id is required")
	}
	return s.createOperation(ctx, OperationRecord{Type: "restore", TrashIDs: ids})
}
func (s *TreeStore) CreateDeleteTrashOperation(ctx context.Context, ids []string) (OperationRecord, error) {
	return s.createOperation(ctx, OperationRecord{Type: "delete-trash", TrashIDs: ids})
}
func (s *TreeStore) GetOperation(ctx context.Context, id string) (OperationRecord, bool, error) {
	o, ok, e := s.objects.Get(ctx, s.operationKey(id))
	if e != nil || !ok {
		return OperationRecord{}, ok, e
	}
	var op OperationRecord
	e = json.Unmarshal(o.Data, &op)
	return op, true, e
}
func (s *TreeStore) ListRunnableOperations(ctx context.Context) ([]OperationRecord, error) {
	all, e := s.listOperations(ctx)
	if e != nil {
		return nil, e
	}
	var out []OperationRecord
	for _, op := range all {
		if op.Status == "pending" || op.Status == "running" {
			out = append(out, op)
		}
	}
	return out, nil
}

func (s *TreeStore) listOperations(ctx context.Context) ([]OperationRecord, error) {
	keys, e := s.objects.List(ctx, s.prefix+"/operations/")
	if e != nil {
		return nil, e
	}
	var out []OperationRecord
	for _, key := range keys {
		o, ok, e := s.objects.Get(ctx, key)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		var op OperationRecord
		if e = json.Unmarshal(o.Data, &op); e != nil {
			return nil, e
		}
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (s *TreeStore) RunOperation(ctx context.Context, id string) (op OperationRecord, err error) {
	if s.v4 != nil {
		return s.v4.runOperation(ctx, id)
	}
	op, g, ok, e := s.loadOperation(ctx, id)
	if e != nil {
		return op, e
	}
	if !ok {
		return op, ErrNotFound
	}
	if op.Status == "completed" {
		return op, nil
	}
	if op.Status == "running" && op.LeaseUntil != nil && op.LeaseUntil.After(time.Now()) {
		return op, fmt.Errorf("operation is already running")
	}
	owner := uuid.NewString()
	until := time.Now().UTC().Add(2 * time.Minute)
	op.Status = "running"
	op.LeaseOwner = owner
	op.LeaseUntil = &until
	if g, e = s.saveOperationCAS(ctx, op, g); e != nil {
		return op, e
	}
	defer func() {
		if err != nil {
			current, cg, found, loadErr := s.loadOperation(context.Background(), op.ID)
			if loadErr == nil && found && current.LeaseOwner == owner {
				current.Status = "pending"
				current.Error = err.Error()
				current.LeaseOwner = ""
				current.LeaseUntil = nil
				_, _ = s.saveOperationCAS(context.Background(), current, cg)
			}
		}
	}()
	var records []FileRecord
	summary := append([]FileRecord(nil), op.Result...)
	switch op.Type {
	case "move":
		records, e = s.runMove(ctx, op.Paths, op.Destination, &op)
	case "rename":
		if len(op.Paths) != 1 || op.Destination == "" {
			return op, fmt.Errorf("invalid rename operation")
		}
		records, e = s.runRename(ctx, op.Paths[0], op.Destination, &op)
	case "trash":
		if op.Total == 0 {
			for _, item := range op.TrashItems {
				root, ok, countErr := s.Find(ctx, item.Path)
				if countErr != nil {
					return op, countErr
				}
				if ok {
					sub, countErr := s.collectSubtree(ctx, root)
					if countErr != nil {
						return op, countErr
					}
					op.Total += len(sub)
					for _, r := range sub {
						if r.LogicPath == cleanLogicPath(item.Path) {
							rootSummary := r
							at := time.Now().UTC()
							rootSummary.TrashedAt = &at
							rootSummary.TrashID = item.TrashID
							rootSummary.TrashRoot = true
							summary = append(summary, rootSummary)
						}
						if r.IsDirectory {
							op.StatsDelta.LogicalDirs--
						} else {
							op.StatsDelta.LogicalFiles--
							op.StatsDelta.LogicalBytes -= r.Size
							op.StatsDelta.PhysicalObjects--
							op.StatsDelta.PhysicalBytes -= r.Size
						}
					}
				}
			}
			op.Result = summary
			if e = s.checkpointOperation(ctx, &op); e != nil {
				return op, e
			}
		}
		records, e = s.trashPathsInternal(ctx, op.TrashItems, func() error {
			op.Progress++
			if op.Progress%16 == 0 {
				return s.checkpointOperation(ctx, &op)
			}
			return nil
		}, false)
		if e == nil && len(summary) == 0 {
			for _, item := range op.TrashItems {
				if m, ok, _ := s.GetTrashManifest(ctx, item.TrashID); ok {
					summary = append(summary, m.Root)
				}
			}
		}
	case "restore":
		if len(summary) == 0 {
			for _, id := range op.TrashIDs {
				if m, ok, _ := s.GetTrashManifest(ctx, id); ok {
					summary = append(summary, m.Root)
				}
			}
		}
		if op.Total == 0 {
			pending, countErr := s.ListTrashRecords(ctx, op.TrashIDs)
			if countErr != nil {
				return op, countErr
			}
			op.Total = len(pending)
			for _, r := range pending {
				if r.IsDirectory {
					op.StatsDelta.LogicalDirs++
				} else {
					op.StatsDelta.LogicalFiles++
					op.StatsDelta.LogicalBytes += r.Size
					op.StatsDelta.PhysicalObjects++
					op.StatsDelta.PhysicalBytes += r.Size
				}
			}
			op.Result = summary
			if e = s.checkpointOperation(ctx, &op); e != nil {
				return op, e
			}
		}
		records, e = s.restoreTrashInternal(ctx, op.TrashIDs, func() error {
			op.Progress++
			if op.Progress%16 == 0 {
				return s.checkpointOperation(ctx, &op)
			}
			return nil
		}, false)
	case "delete-trash":
		return op, fmt.Errorf("delete-trash operation requires physical object executor")
	default:
		return op, fmt.Errorf("unknown operation type %q", op.Type)
	}
	if e != nil {
		return op, e
	}
	if op.Type == "trash" || op.Type == "restore" {
		if e = s.mutateStatsOnce(ctx, op.ID, op.StatsDelta); e != nil {
			return op, e
		}
		records = summary
	}
	op.Result = records
	op.Progress = op.Total
	op.Status = "completed"
	op.Error = ""
	op.LeaseOwner = ""
	op.LeaseUntil = nil
	current, cg, found, e := s.loadOperation(ctx, op.ID)
	if e != nil {
		return op, e
	}
	if !found {
		return op, ErrNotFound
	}
	if current.LeaseOwner != owner {
		return current, fmt.Errorf("operation lease lost")
	}
	current.Result = op.Result
	current.Progress = op.Progress
	current.Total = op.Total
	current.StatsDelta = op.StatsDelta
	current.Status = op.Status
	current.Error = ""
	current.LeaseOwner = ""
	current.LeaseUntil = nil
	_, e = s.saveOperationCAS(ctx, current, cg)
	op = current
	if e == nil && (op.Type == "trash" || op.Type == "restore") {
		_ = s.removeStatsToken(ctx, op.ID)
	}
	return op, e
}
func (s *TreeStore) checkpointOperation(ctx context.Context, op *OperationRecord) error {
	current, g, ok, e := s.loadOperation(ctx, op.ID)
	if e != nil {
		return e
	}
	if !ok {
		return ErrNotFound
	}
	if current.LeaseOwner != op.LeaseOwner {
		return fmt.Errorf("operation lease lost")
	}
	current.Progress = op.Progress
	current.Total = op.Total
	renew := time.Now().UTC().Add(2 * time.Minute)
	current.LeaseUntil = &renew
	_, e = s.saveOperationCAS(ctx, current, g)
	if e == nil {
		op.LeaseUntil = &renew
	}
	return e
}

func (s *TreeStore) BatchMove(ctx context.Context, paths []string, destination string) ([]FileRecord, error) {
	if s.v4 != nil {
		return s.v4.movePaths(ctx, paths, destination)
	}
	op, e := s.CreateMoveOperation(ctx, paths, destination)
	if e != nil {
		return nil, e
	}
	op, e = s.RunOperation(ctx, op.ID)
	return op.Result, e
}
func (s *TreeStore) runMove(ctx context.Context, paths []string, destination string, op *OperationRecord) ([]FileRecord, error) {
	return s.runRelocate(ctx, paths, destination, "", op)
}

func (s *TreeStore) runRename(ctx context.Context, from, to string, op *OperationRecord) ([]FileRecord, error) {
	return s.runRelocate(ctx, []string{from}, "", to, op)
}

// runRelocate persists each node before removing its source. This ordering lets
// a later runner recognize an already-written target after a worker crash.
// explicitTarget is used only by rename; ordinary moves derive one target per
// source root below the requested destination directory.
func (s *TreeStore) runRelocate(ctx context.Context, paths []string, destination, explicitTarget string, op *OperationRecord) ([]FileRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, renewLease, e := s.acquireTreeMutationLease(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	roots, e := normalizeRoots(paths)
	if e != nil {
		return nil, e
	}
	if explicitTarget == "" {
		destination = cleanLogicPath(destination)
	} else {
		explicitTarget = cleanLogicPath(explicitTarget)
		destination = parentLogicPath(explicitTarget)
	}
	if destination != "" {
		r, ok, e := s.Find(ctx, destination)
		if e != nil {
			return nil, e
		}
		if !ok || !r.IsDirectory {
			return nil, fmt.Errorf("destination directory not found: %s", destination)
		}
	}
	targetForRoot := func(rootPath string) string {
		if explicitTarget != "" {
			return explicitTarget
		}
		return joinLogicPath(destination, pathpkg.Base(rootPath))
	}
	var results []FileRecord
	targetRoots := map[string]string{}
	for _, rootPath := range roots {
		targetRoot := targetForRoot(rootPath)
		if previous, exists := targetRoots[targetRoot]; exists && previous != rootPath {
			return nil, fmt.Errorf("%w: %s", ErrPathConflict, targetRoot)
		}
		targetRoots[targetRoot] = rootPath
		root, ok, e := s.Find(ctx, rootPath)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		records, e := s.collectSubtree(ctx, root)
		if e != nil {
			return nil, e
		}
		for _, old := range records {
			targetPath := targetRoot + strings.TrimPrefix(old.LogicPath, rootPath)
			if e = validateMetadataKey(s.activeKey(targetPath, old.IsDirectory)); e != nil {
				return nil, e
			}
			if current, found, e := s.Find(ctx, targetPath); e != nil {
				return nil, e
			} else if found && (current.ID != old.ID || current.PhysicalHash != old.PhysicalHash || current.IsDirectory != old.IsDirectory) {
				return nil, fmt.Errorf("%w: %s", ErrPathConflict, targetPath)
			}
		}
	}
	if op.Total == 0 {
		for _, rootPath := range roots {
			root, ok, e := s.Find(ctx, rootPath)
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
			records, e := s.collectSubtree(ctx, root)
			if e != nil {
				return nil, e
			}
			op.Total += len(records)
		}
		if e = s.checkpointOperation(ctx, op); e != nil {
			return nil, e
		}
	}
	for _, rootPath := range roots {
		root, ok, e := s.Find(ctx, rootPath)
		if e != nil {
			return nil, e
		}
		targetRoot := targetForRoot(rootPath)
		if !ok {
			if existing, found, e := s.Find(ctx, targetRoot); e == nil && found {
				old := existing
				old.LogicPath = rootPath
				_ = s.updateIndexRecord(ctx, parentLogicPath(rootPath), old, true)
				if old.IsDirectory {
					_ = s.deleteIndexManifest(ctx, rootPath)
				}
				results = append(results, existing)
				continue
			}
			return nil, ErrNotFound
		}
		if targetRoot == rootPath || (root.IsDirectory && strings.HasPrefix(targetRoot, withTrailingSlash(rootPath))) {
			return nil, ErrInvalidMove
		}
		records, e := s.collectSubtree(ctx, root)
		if e != nil {
			return nil, e
		}
		for _, old := range records {
			target := old
			target.LogicPath = targetRoot + strings.TrimPrefix(old.LogicPath, rootPath)
			target.UpdatedAt = time.Now().UTC()
			if current, found, e := s.Find(ctx, target.LogicPath); e != nil {
				return nil, e
			} else if found && (current.ID != target.ID || current.PhysicalHash != target.PhysicalHash || current.IsDirectory != target.IsDirectory) {
				return nil, fmt.Errorf("%w: %s", ErrPathConflict, target.LogicPath)
			}
		}
		for _, old := range records {
			target := old
			target.LogicPath = targetRoot + strings.TrimPrefix(old.LogicPath, rootPath)
			target.UpdatedAt = time.Now().UTC()
			if _, found, _ := s.Find(ctx, target.LogicPath); !found {
				if e = s.putNode(ctx, target, true); e != nil {
					return nil, e
				}
			}
			if e = s.updateIndexRecord(ctx, parentLogicPath(target.LogicPath), target, false); e != nil {
				return nil, e
			}
			if _, found, _ := s.Find(ctx, old.LogicPath); found {
				if e = s.deleteNode(ctx, old); e != nil {
					return nil, e
				}
			}
			if e = s.updateIndexRecord(ctx, parentLogicPath(old.LogicPath), old, true); e != nil {
				return nil, e
			}
			if old.IsDirectory {
				if e = s.deleteIndexManifest(ctx, old.LogicPath); e != nil {
					return nil, e
				}
			}
			op.Progress++
			if op.Progress%16 == 0 {
				if e = renewLease(ctx); e != nil {
					return nil, e
				}
				if e = s.checkpointOperation(ctx, op); e != nil {
					return nil, e
				}
			}
		}
		movedRoot := root
		movedRoot.LogicPath = targetRoot
		results = append(results, movedRoot)
	}
	var rebuild, propagate []string
	for _, rootPath := range roots {
		propagate = append(propagate, parentLogicPath(rootPath))
		targetRoot := targetForRoot(rootPath)
		if target, found, findErr := s.Find(ctx, targetRoot); findErr != nil {
			return nil, findErr
		} else if found && target.IsDirectory {
			targetRecords, collectErr := s.collectSubtree(ctx, target)
			if collectErr != nil {
				return nil, collectErr
			}
			rebuild = append(rebuild, directoryPaths(targetRecords)...)
		}
	}
	propagate = append(propagate, destination)
	if e = s.repairOperationAggregatesLeaseHeld(ctx, rebuild, propagate); e != nil {
		return nil, e
	}
	return results, nil
}
func (s *TreeStore) collectSubtree(ctx context.Context, root FileRecord) ([]FileRecord, error) {
	if !root.IsDirectory {
		return []FileRecord{root}, nil
	}
	var out []FileRecord
	var walk func(FileRecord) error
	walk = func(dir FileRecord) error {
		idx, _, ok, e := s.getIndex(ctx, dir.LogicPath)
		if e != nil {
			return e
		}
		if ok {
			for _, child := range idx.Records {
				if child.IsDirectory {
					if e = walk(child); e != nil {
						return e
					}
				} else {
					out = append(out, child)
				}
			}
		}
		out = append(out, dir)
		return nil
	}
	e := walk(root)
	return out, e
}
func (s *TreeStore) RenamePath(ctx context.Context, from, to string) error {
	if s.v4 != nil {
		return s.v4.renamePath(ctx, from, to)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, _, e := s.acquireTreeMutationLease(ctx)
	if e != nil {
		return e
	}
	defer release()
	from = cleanLogicPath(from)
	to = cleanLogicPath(to)
	root, ok, e := s.Find(ctx, from)
	if e != nil {
		return e
	}
	if !ok {
		return ErrNotFound
	}
	if to == from || (root.IsDirectory && strings.HasPrefix(to, withTrailingSlash(from))) {
		return ErrInvalidMove
	}
	records, e := s.collectSubtree(ctx, root)
	if e != nil {
		return e
	}
	for _, old := range records {
		target := old
		target.LogicPath = to + strings.TrimPrefix(old.LogicPath, from)
		if current, found, e := s.Find(ctx, target.LogicPath); e != nil {
			return e
		} else if found && current.ID != old.ID {
			return fmt.Errorf("%w: %s", ErrPathConflict, target.LogicPath)
		}
		if e = validateMetadataKey(s.activeKey(target.LogicPath, target.IsDirectory)); e != nil {
			return e
		}
	}
	for _, old := range records {
		target := old
		target.LogicPath = to + strings.TrimPrefix(old.LogicPath, from)
		target.UpdatedAt = time.Now().UTC()
		if _, found, _ := s.Find(ctx, target.LogicPath); !found {
			if e = s.putNode(ctx, target, true); e != nil {
				return e
			}
		}
		if e = s.updateIndexRecord(ctx, parentLogicPath(target.LogicPath), target, false); e != nil {
			return e
		}
		if _, found, _ := s.Find(ctx, old.LogicPath); found {
			if e = s.deleteNode(ctx, old); e != nil {
				return e
			}
		}
		if e = s.updateIndexRecord(ctx, parentLogicPath(old.LogicPath), old, true); e != nil {
			return e
		}
		if old.IsDirectory {
			if e = s.deleteIndexManifest(ctx, old.LogicPath); e != nil {
				return e
			}
		}
	}
	var rebuild []string
	if target, found, findErr := s.Find(ctx, to); findErr != nil {
		return findErr
	} else if found && target.IsDirectory {
		targetRecords, collectErr := s.collectSubtree(ctx, target)
		if collectErr != nil {
			return collectErr
		}
		rebuild = directoryPaths(targetRecords)
	}
	if e = s.repairOperationAggregatesLeaseHeld(ctx, rebuild, []string{parentLogicPath(from), parentLogicPath(to)}); e != nil {
		return e
	}
	return nil
}

// RenameSameParentV4 returns the committed record already observed by the v4
// transaction. The boolean is false for v3 stores and cross-directory moves so
// callers can preserve the legacy validation and operation behavior.
func (s *TreeStore) RenameSameParentV4(ctx context.Context, from, to string) (FileRecord, bool, error) {
	if s.v4 == nil {
		return FileRecord{}, false, nil
	}
	from, to = cleanLogicPath(from), cleanLogicPath(to)
	if from == "" || to == "" || from == to || parentLogicPath(from) != parentLogicPath(to) {
		return FileRecord{}, false, nil
	}
	node, err := s.v4.renameSameParentOptimistic(ctx, parentLogicPath(from), pathpkg.Base(from), pathpkg.Base(to))
	if err != nil {
		return FileRecord{}, true, err
	}
	return v4FileRecord(to, node), true, nil
}

type trashManifest struct {
	Version   int        `json:"version"`
	ID        string     `json:"id"`
	Root      FileRecord `json:"root"`
	Deleting  bool       `json:"deleting"`
	CreatedAt time.Time  `json:"createdAt"`
}

// repairOperationAggregatesLeaseHeld rebuilds touched directory manifests
// deepest-first, then publishes the stable absolute summaries through their
// ancestors. It is safe to rerun after a crash and costs O(touched directories
// + touched roots*depth), rather than O(moved nodes*depth).
func (s *TreeStore) repairOperationAggregatesLeaseHeld(ctx context.Context, rebuild, propagate []string) error {
	uniqueRebuild := map[string]bool{}
	for _, dir := range rebuild {
		uniqueRebuild[cleanLogicPath(dir)] = true
	}
	dirs := make([]string, 0, len(uniqueRebuild))
	for dir := range uniqueRebuild {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		left := strings.Count(strings.Trim(dirs[i], "/"), "/")
		right := strings.Count(strings.Trim(dirs[j], "/"), "/")
		if left == right {
			return dirs[i] > dirs[j]
		}
		return left > right
	})
	for _, dir := range dirs {
		idx, generation, exists, err := s.getIndex(ctx, dir)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err = s.writeIndex(ctx, idx, generation, true); err != nil {
			return err
		}
	}
	uniquePropagate := map[string]bool{}
	for _, dir := range propagate {
		uniquePropagate[cleanLogicPath(dir)] = true
	}
	for dir := range uniquePropagate {
		if err := s.propagateDirectorySummaryLeaseHeld(ctx, dir); err != nil {
			return err
		}
	}
	return nil
}

func directoryPaths(records []FileRecord) []string {
	var dirs []string
	for _, record := range records {
		if record.IsDirectory {
			dirs = append(dirs, record.LogicPath)
		}
	}
	return dirs
}

func (s *TreeStore) trashManifestKey(id string) string { return s.trashPrefix(id) + "manifest.json" }
func (s *TreeStore) TrashPaths(ctx context.Context, items []TrashPath) ([]FileRecord, error) {
	if s.v4 != nil {
		return s.v4.trashPaths(ctx, items)
	}
	return s.trashPathsInternal(ctx, items, nil, true)
}
func (s *TreeStore) trashPathsInternal(ctx context.Context, items []TrashPath, checkpoint func() error, applyStats bool) ([]FileRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, renewLease, e := s.acquireTreeMutationLease(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	if len(items) == 0 {
		return nil, fmt.Errorf("at least one path is required")
	}
	paths := make([]string, len(items))
	ids := map[string]string{}
	for i, item := range items {
		paths[i] = item.Path
		ids[cleanLogicPath(item.Path)] = item.TrashID
	}
	roots, e := normalizeRoots(paths)
	if e != nil {
		return nil, e
	}
	now := time.Now().UTC()
	var updated []FileRecord
	processed := 0
	for _, rootPath := range roots {
		trashID := ids[rootPath]
		if trashID == "" {
			return nil, fmt.Errorf("trash id is required")
		}
		root, ok, e := s.Find(ctx, rootPath)
		if e != nil {
			return nil, e
		}
		if !ok {
			if manifest, exists, manifestErr := s.GetTrashManifest(ctx, trashID); manifestErr != nil {
				return nil, manifestErr
			} else if exists {
				active := manifest.Root
				active.TrashedAt = nil
				active.TrashID = ""
				active.TrashRoot = false
				_ = s.updateIndexRecord(ctx, parentLogicPath(active.LogicPath), active, true)
				if active.IsDirectory {
					_ = s.deleteIndexManifest(ctx, active.LogicPath)
				}
				continue
			}
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rootPath)
		}
		if root.IsDirectory {
			idx, _, exists, indexErr := s.getIndexManifest(ctx, root.LogicPath)
			if indexErr != nil {
				return nil, indexErr
			}
			summary := FolderSummary{}
			if exists {
				summary = folderSummaryFromIndex(idx)
			}
			root.FolderSummary = &summary
		}
		records, e := s.collectSubtree(ctx, root)
		if e != nil {
			return nil, e
		}
		for _, r := range records {
			at := now
			r.TrashedAt = &at
			r.TrashID = trashID
			r.TrashRoot = r.LogicPath == rootPath
			r.UpdatedAt = now
			key := s.trashNodeKey(trashID, r)
			if e = validateMetadataKey(key); e != nil {
				return nil, e
			}
			updated = append(updated, r)
		}
	}
	for _, rootPath := range roots {
		trashID := ids[rootPath]
		var root FileRecord
		for _, r := range updated {
			if r.TrashID == trashID && r.TrashRoot {
				root = r
				break
			}
		}
		if root.ID == 0 {
			continue
		}
		m := trashManifest{Version: 2, ID: trashID, Root: root, CreatedAt: time.Now().UTC()}
		b, _ := marshalTree(m)
		z := int64(0)
		manifestKey := s.trashManifestKey(trashID)
		if existing, ok, getErr := s.objects.Get(ctx, manifestKey); getErr != nil {
			return nil, getErr
		} else if ok {
			var current trashManifest
			if getErr = json.Unmarshal(existing.Data, &current); getErr != nil {
				return nil, getErr
			}
			if current.Root.ID != root.ID {
				return nil, ErrPathConflict
			}
		} else if _, e = s.objects.Put(ctx, manifestKey, b, &z); e != nil {
			return nil, e
		}
	}
	for _, r := range updated {
		b, _ := marshalTree(r)
		z := int64(0)
		key := s.trashNodeKey(r.TrashID, r)
		if e = validateMetadataKey(key); e != nil {
			return nil, e
		}
		if _, exists, getErr := s.objects.Get(ctx, key); getErr != nil {
			return nil, getErr
		} else if !exists {
			if _, e = s.objects.Put(ctx, key, b, &z); e != nil {
				return nil, e
			}
		}
		active := r
		active.TrashedAt = nil
		active.TrashID = ""
		active.TrashRoot = false
		if e = s.deleteNode(ctx, active); e != nil {
			return nil, e
		}
		if e = s.updateIndexRecord(ctx, parentLogicPath(active.LogicPath), active, true); e != nil {
			return nil, e
		}
		if active.IsDirectory {
			if e = s.deleteIndexManifest(ctx, active.LogicPath); e != nil {
				return nil, e
			}
		}
		delta := MetadataStats{}
		if active.IsDirectory {
			delta.LogicalDirs = -1
		} else {
			delta.LogicalFiles = -1
			delta.LogicalBytes = -active.Size
			delta.PhysicalObjects = -1
			delta.PhysicalBytes = -active.Size
		}
		if applyStats {
			if e = s.mutateStats(ctx, delta); e != nil {
				return nil, e
			}
		}
		if checkpoint != nil {
			if e = checkpoint(); e != nil {
				return nil, e
			}
		}
		processed++
		if processed%16 == 0 {
			if e = renewLease(ctx); e != nil {
				return nil, e
			}
		}
	}
	var propagate []string
	for _, rootPath := range roots {
		propagate = append(propagate, parentLogicPath(rootPath))
	}
	if e = s.repairOperationAggregatesLeaseHeld(ctx, nil, propagate); e != nil {
		return nil, e
	}
	return updated, nil
}
func (s *TreeStore) listTrashManifests(ctx context.Context) ([]trashManifest, error) {
	keys, e := s.objects.List(ctx, s.prefix+"/trash/")
	if e != nil {
		return nil, e
	}
	var out []trashManifest
	for _, key := range keys {
		if !strings.HasSuffix(key, "/manifest.json") {
			continue
		}
		o, ok, e := s.objects.Get(ctx, key)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		var m trashManifest
		if e = json.Unmarshal(o.Data, &m); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, nil
}
func (s *TreeStore) ListTrash(ctx context.Context) ([]FileRecord, error) {
	if s.v4 != nil {
		return s.v4.listTrash(ctx)
	}
	ms, e := s.listTrashManifests(ctx)
	if e != nil {
		return nil, e
	}
	var out []FileRecord
	for _, m := range ms {
		out = append(out, m.Root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TrashedAt.After(*out[j].TrashedAt) })
	return out, nil
}
func (s *TreeStore) ListTrashRecords(ctx context.Context, ids []string) ([]FileRecord, error) {
	if s.v4 != nil {
		return s.v4.listTrashRecords(ctx, ids)
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	ms, e := s.listTrashManifests(ctx)
	if e != nil {
		return nil, e
	}
	var out []FileRecord
	for _, m := range ms {
		if len(wanted) > 0 && !wanted[m.ID] {
			continue
		}
		for _, kind := range []string{"files/", "directories/"} {
			keys, e := s.objects.List(ctx, s.trashPrefix(m.ID)+kind)
			if e != nil {
				return nil, e
			}
			for _, key := range keys {
				o, ok, e := s.objects.Get(ctx, key)
				if e != nil {
					return nil, e
				}
				if !ok {
					continue
				}
				r, e := decodeTreeRecord(o)
				if e != nil {
					return nil, e
				}
				out = append(out, r)
			}
		}
	}
	return out, nil
}
func cleanTrashIDs(ids []string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			m[id] = true
		}
	}
	return m
}
func (s *TreeStore) RestoreTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	if s.v4 != nil {
		return s.v4.restoreTrash(ctx, ids)
	}
	return s.restoreTrashInternal(ctx, ids, nil, true)
}
func (s *TreeStore) restoreTrashInternal(ctx context.Context, ids []string, checkpoint func() error, applyStats bool) ([]FileRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, renewLease, e := s.acquireTreeMutationLease(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	wanted := cleanTrashIDs(ids)
	if len(wanted) == 0 {
		return nil, fmt.Errorf("at least one trash id is required")
	}
	manifests := make([]trashManifest, 0, len(wanted))
	for id := range wanted {
		m, ok, e := s.GetTrashManifest(ctx, id)
		if e != nil {
			return nil, e
		}
		if !ok {
			if !applyStats {
				continue
			}
			return nil, ErrNotFound
		}
		if m.Deleting {
			return nil, ErrTrashBusy
		}
		manifests = append(manifests, m)
	}
	records, e := s.ListTrashRecords(ctx, ids)
	if e != nil {
		return nil, e
	}
	found := map[string]bool{}
	if !applyStats {
		for id := range wanted {
			found[id] = true
		}
	}
	for _, r := range records {
		found[r.TrashID] = true
		if r.TrashDeleting {
			return nil, ErrTrashBusy
		}
		if active, ok, e := s.Find(ctx, r.LogicPath); e != nil {
			return nil, e
		} else if ok && active.ID != r.ID {
			return nil, fmt.Errorf("%w: %s", ErrPathConflict, r.LogicPath)
		}
	}
	for id := range wanted {
		if !found[id] {
			return nil, ErrNotFound
		}
	}
	now := time.Now().UTC()
	var restored []FileRecord
	processed := 0
	for _, r := range records {
		old := r
		r.TrashedAt = nil
		r.TrashID = ""
		r.TrashRoot = false
		r.TrashDeleting = false
		r.UpdatedAt = now
		_, already, _ := s.Find(ctx, r.LogicPath)
		if !already {
			if e = s.putNode(ctx, r, true); e != nil {
				return nil, e
			}
		}
		if e = s.updateIndexRecord(ctx, parentLogicPath(r.LogicPath), r, false); e != nil {
			return nil, e
		}
		if r.IsDirectory {
			if _, _, exists, indexErr := s.getIndexManifest(ctx, r.LogicPath); indexErr != nil {
				return nil, indexErr
			} else if !exists {
				if indexErr = s.writeIndex(ctx, directoryIndex{Version: 1, Directory: r.LogicPath}, 0, false); indexErr != nil {
					return nil, indexErr
				}
			}
		}
		if e = s.objects.Delete(ctx, s.trashNodeKey(old.TrashID, old), nil); e != nil {
			return nil, e
		}
		if !already && applyStats {
			delta := MetadataStats{}
			if r.IsDirectory {
				delta.LogicalDirs = 1
			} else {
				delta.LogicalFiles = 1
				delta.LogicalBytes = r.Size
				delta.PhysicalObjects = 1
				delta.PhysicalBytes = r.Size
			}
			if e = s.mutateStats(ctx, delta); e != nil {
				return nil, e
			}
		}
		restored = append(restored, r)
		if checkpoint != nil {
			if e = checkpoint(); e != nil {
				return nil, e
			}
		}
		processed++
		if processed%16 == 0 {
			if e = renewLease(ctx); e != nil {
				return nil, e
			}
		}
	}
	var rebuild, propagate []string
	for _, manifest := range manifests {
		rootPath := manifest.Root.LogicPath
		// The restored root's parent entry was written before all descendant
		// manifests were rebuilt, so publish the root itself after repair.
		propagate = append(propagate, rootPath)
		if root, exists, findErr := s.Find(ctx, rootPath); findErr != nil {
			return nil, findErr
		} else if exists && root.IsDirectory {
			activeRecords, collectErr := s.collectSubtree(ctx, root)
			if collectErr != nil {
				return nil, collectErr
			}
			rebuild = append(rebuild, directoryPaths(activeRecords)...)
		}
	}
	if e = s.repairOperationAggregatesLeaseHeld(ctx, rebuild, propagate); e != nil {
		return nil, e
	}
	for id := range wanted {
		_ = s.objects.Delete(ctx, s.trashManifestKey(id), nil)
	}
	return restored, nil
}
func (s *TreeStore) ClaimTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	if s.v4 != nil {
		return s.v4.claimTrash(ctx, ids)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ms, e := s.listTrashManifests(ctx)
	if e != nil {
		return nil, e
	}
	wanted := cleanTrashIDs(ids)
	all := len(ids) == 0
	found := map[string]bool{}
	for _, m := range ms {
		if !all && !wanted[m.ID] {
			continue
		}
		found[m.ID] = true
		m.Deleting = true
		m.Root.TrashDeleting = true
		b, _ := marshalTree(m)
		if _, e = s.objects.Put(ctx, s.trashManifestKey(m.ID), b, nil); e != nil {
			return nil, e
		}
	}
	if !all {
		for id := range wanted {
			if !found[id] {
				return nil, ErrNotFound
			}
		}
	}
	records, e := s.ListTrashRecords(ctx, ids)
	for i := range records {
		records[i].TrashDeleting = true
	}
	return records, e
}
func (s *TreeStore) DeleteTrash(ctx context.Context, ids []string) (int64, error) {
	if s.v4 != nil {
		return s.v4.deleteTrash(ctx, ids)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := cleanTrashIDs(ids)
	if len(wanted) == 0 {
		return 0, fmt.Errorf("at least one trash id is required")
	}
	var count int64
	for id := range wanted {
		m, ok, e := s.GetTrashManifest(ctx, id)
		if e != nil {
			return count, e
		}
		if !ok {
			return count, ErrNotFound
		}
		if !m.Deleting {
			return count, ErrTrashBusy
		}
		keys, e := s.objects.List(ctx, s.trashPrefix(id))
		if e != nil {
			return count, e
		}
		for _, key := range keys {
			if !strings.HasSuffix(key, "/manifest.json") {
				count++
			}
			if e = s.objects.Delete(ctx, key, nil); e != nil {
				return count, e
			}
		}
	}
	return count, nil
}
func (s *TreeStore) GetTrashManifest(ctx context.Context, id string) (trashManifest, bool, error) {
	o, ok, e := s.objects.Get(ctx, s.trashManifestKey(id))
	if e != nil || !ok {
		return trashManifest{}, ok, e
	}
	var m trashManifest
	e = json.Unmarshal(o.Data, &m)
	return m, true, e
}
