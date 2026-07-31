// Package drift detects mismatches between logical paths and physical object
// keys and executes explicit, restartable reconciliation plans.
package drift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

type Status string

const (
	Aligned        Status = "aligned"
	Drifted        Status = "drifted"
	ObjectMissing  Status = "object_missing"
	SizeMismatch   Status = "size_mismatch"
	TargetConflict Status = "target_conflict"
	OrphanObject   Status = "orphan_object"
	SharedObject   Status = "shared_object"
)

const (
	ScopeActive = "active"
	ScopeTrash  = "trash"
	ScopeObject = "object"
)

type Entry struct {
	LogicPath    string            `json:"logicPath,omitempty"`
	PhysicalHash string            `json:"physicalHash"`
	TargetKey    string            `json:"targetKey,omitempty"`
	Size         int64             `json:"size"`
	Scope        string            `json:"scope"`
	Status       Status            `json:"status"`
	Actionable   bool              `json:"actionable"`
	References   int               `json:"references,omitempty"`
	Object       blob.DriftObject  `json:"object,omitempty"`
	Target       *blob.DriftObject `json:"target,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type Snapshot struct {
	Entries     []Entry        `json:"entries"`
	Counts      map[Status]int `json:"counts"`
	GeneratedAt time.Time      `json:"generatedAt"`
	ObjectRoot  string         `json:"objectRoot"`
}

type CostItem struct {
	Name    string  `json:"name"`
	Units   float64 `json:"units"`
	USDMin  float64 `json:"usdMin"`
	USDMax  float64 `json:"usdMax"`
	Details string  `json:"details"`
}

type CostEstimate struct {
	Currency    string     `json:"currency"`
	USDMin      float64    `json:"usdMin"`
	USDMax      float64    `json:"usdMax"`
	Breakdown   []CostItem `json:"breakdown"`
	PricingAsOf string     `json:"pricingAsOf"`
	Estimate    bool       `json:"estimate"`
	Warnings    []string   `json:"warnings"`
}

type PlanEntry struct {
	LogicPath string           `json:"logicPath"`
	Source    blob.DriftObject `json:"source"`
	TargetKey string           `json:"targetKey"`
	Shared    bool             `json:"shared"`
}

type Plan struct {
	ID          string       `json:"id"`
	Fingerprint string       `json:"fingerprint"`
	Entries     []PlanEntry  `json:"entries"`
	Cost        CostEstimate `json:"cost"`
	CreatedAt   time.Time    `json:"createdAt"`
}

type Action struct {
	ID             string            `json:"id"`
	PlanID         string            `json:"planId"`
	IdempotencyKey string            `json:"idempotencyKey"`
	Status         string            `json:"status"`
	Checkpoint     string            `json:"checkpoint"`
	EntryIndex     int               `json:"entryIndex"`
	Target         *blob.DriftObject `json:"target,omitempty"`
	Error          string            `json:"error,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	CompletedAt    *time.Time        `json:"completedAt,omitempty"`
	LeaseUntil     *time.Time        `json:"leaseUntil,omitempty"`
	Version        int64             `json:"-"`
}

type targetKeyFunc func(string) (string, error)

type Service struct {
	metadata db.Store
	objects  blob.DriftObjectStore
	state    db.DriftStateStore
	root     string
	target   targetKeyFunc
	autoKick bool

	mu       sync.RWMutex
	snapshot *Snapshot
}

func New(metadata db.Store, objects blob.Store) (*Service, error) {
	driftObjects, ok := objects.(blob.DriftObjectStore)
	if !ok {
		return nil, fmt.Errorf("object backend %T does not support drift operations", objects)
	}
	state, err := db.AsDriftStateStore(metadata)
	if err != nil {
		return nil, err
	}
	return &Service{metadata: metadata, objects: driftObjects, state: state, root: objects.Root(), target: objectkey.FromLogicalPath, autoKick: true}, nil
}

// NewForTest keeps failure-injection tests independent from concrete storage.
func NewForTest(metadata db.Store, objects blob.DriftObjectStore, state db.DriftStateStore, target targetKeyFunc) *Service {
	return &Service{metadata: metadata, objects: objects, state: state, root: "test", target: target}
}

var ErrNoSnapshot = errors.New("no drift snapshot; explicitly request refresh=true")

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	s.mu.RLock()
	if s.snapshot != nil {
		result := cloneSnapshot(*s.snapshot)
		s.mu.RUnlock()
		return result, nil
	}
	s.mu.RUnlock()
	payload, ok, err := s.state.LoadDriftSnapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !ok {
		return Snapshot{}, ErrNoSnapshot
	}
	var snapshot Snapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode persisted drift snapshot: %w", err)
	}
	s.mu.Lock()
	s.snapshot = &snapshot
	s.mu.Unlock()
	return cloneSnapshot(snapshot), nil
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Entries = append([]Entry(nil), in.Entries...)
	out.Counts = make(map[Status]int, len(in.Counts))
	for k, v := range in.Counts {
		out.Counts[k] = v
	}
	return out
}

// Refresh performs exactly one logical metadata scan and one paged object
// listing. In particular, it never calls Stat once per object.
func (s *Service) Refresh(ctx context.Context) (Snapshot, error) {
	var active, trash []db.FileRecord
	var err error
	if scanner, ok := s.metadata.(db.DriftRecordScanner); ok {
		active, trash, err = scanner.ScanDriftRecords(ctx)
	} else {
		active, err = s.metadata.ListAll(ctx)
		if err == nil {
			trash, err = s.metadata.ListTrashRecords(ctx, nil)
		}
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("scan metadata: %w", err)
	}
	objects, err := s.objects.ListDriftObjects(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	byName := make(map[string]blob.DriftObject, len(objects))
	refs := make(map[string]int, len(active)+len(trash))
	for _, o := range objects {
		byName[o.Name] = o
	}
	for _, records := range [][]db.FileRecord{active, trash} {
		for _, r := range records {
			if !r.IsDirectory && r.PhysicalHash != "" {
				refs[r.PhysicalHash]++
			}
		}
	}

	snapshot := Snapshot{Counts: map[Status]int{}, GeneratedAt: time.Now().UTC(), ObjectRoot: s.root}
	appendRecords := func(records []db.FileRecord, scope string) {
		for _, r := range records {
			if r.IsDirectory {
				continue
			}
			e := Entry{LogicPath: r.LogicPath, PhysicalHash: r.PhysicalHash, Size: r.Size, Scope: scope, References: refs[r.PhysicalHash]}
			target, targetErr := s.target(r.LogicPath)
			if targetErr != nil {
				e.Status, e.Error = Drifted, targetErr.Error()
				snapshot.Entries = append(snapshot.Entries, e)
				snapshot.Counts[e.Status]++
				continue
			}
			e.TargetKey = target
			source, sourceOK := byName[r.PhysicalHash]
			e.Object = source
			if !sourceOK {
				e.Status = ObjectMissing
			} else if source.Size != r.Size {
				e.Status = SizeMismatch
			} else if r.PhysicalHash == target {
				e.Status = Aligned
			} else if targetObject, exists := byName[target]; exists {
				e.Status, e.Target = TargetConflict, &targetObject
			} else if refs[r.PhysicalHash] > 1 {
				e.Status = SharedObject
			} else {
				e.Status = Drifted
			}
			e.Actionable = scope == ScopeActive && (e.Status == Drifted || e.Status == SharedObject)
			snapshot.Entries = append(snapshot.Entries, e)
			snapshot.Counts[e.Status]++
		}
	}
	appendRecords(active, ScopeActive)
	appendRecords(trash, ScopeTrash)
	for _, o := range objects {
		if refs[o.Name] == 0 {
			e := Entry{PhysicalHash: o.Name, Size: o.Size, Scope: ScopeObject, Status: OrphanObject, Object: o}
			snapshot.Entries = append(snapshot.Entries, e)
			snapshot.Counts[OrphanObject]++
		}
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool {
		if snapshot.Entries[i].Scope != snapshot.Entries[j].Scope {
			return snapshot.Entries[i].Scope < snapshot.Entries[j].Scope
		}
		return snapshot.Entries[i].LogicPath+snapshot.Entries[i].PhysicalHash < snapshot.Entries[j].LogicPath+snapshot.Entries[j].PhysicalHash
	})
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.state.SaveDriftSnapshot(ctx, payload); err != nil {
		return Snapshot{}, fmt.Errorf("persist drift snapshot: %w", err)
	}
	s.mu.Lock()
	s.snapshot = &snapshot
	s.mu.Unlock()
	return cloneSnapshot(snapshot), nil
}

func (s *Service) CreatePlan(ctx context.Context, paths []string) (Plan, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return Plan{}, err
	}
	wanted := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		wanted[strings.TrimSpace(p)] = struct{}{}
	}
	var entries []PlanEntry
	targets := make(map[string]string, len(paths))
	for _, e := range snapshot.Entries {
		if _, ok := wanted[e.LogicPath]; !ok {
			continue
		}
		if !e.Actionable {
			return Plan{}, fmt.Errorf("path %s is not actionable (%s)", e.LogicPath, e.Status)
		}
		if existingPath, exists := targets[e.TargetKey]; exists && existingPath != e.LogicPath {
			return Plan{}, fmt.Errorf("sanitized target collision: %s and %s both map to %s", existingPath, e.LogicPath, e.TargetKey)
		}
		targets[e.TargetKey] = e.LogicPath
		entries = append(entries, PlanEntry{LogicPath: e.LogicPath, Source: e.Object, TargetKey: e.TargetKey, Shared: e.Status == SharedObject})
		delete(wanted, e.LogicPath)
	}
	if len(wanted) != 0 || len(entries) == 0 {
		return Plan{}, errors.New("all requested paths must exist in the cached snapshot and be actionable")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].LogicPath < entries[j].LogicPath })
	canonical, _ := json.Marshal(entries)
	sum := sha256.Sum256(canonical)
	fingerprint := hex.EncodeToString(sum[:])
	plan := Plan{ID: "plan-" + fingerprint[:32], Fingerprint: fingerprint, Entries: entries, Cost: estimateCost(entries), CreatedAt: time.Now().UTC()}
	payload, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, err
	}
	stored, err := s.state.CreateDriftPlan(ctx, db.DriftPlanRecord{ID: plan.ID, Fingerprint: fingerprint, Payload: payload, CreatedAt: plan.CreatedAt})
	if err != nil {
		return Plan{}, err
	}
	if err := json.Unmarshal(stored.Payload, &plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func estimateCost(entries []PlanEntry) CostEstimate {
	var bytes int64
	var retrieval float64
	var earlyDelete float64
	now := time.Now().UTC()
	for _, e := range entries {
		bytes += e.Source.Size
		gib := float64(e.Source.Size) / (1 << 30)
		minimumDays, retrievalRate, storageRate := storageClassRates(e.Source.StorageClass)
		retrieval += gib * retrievalRate
		if minimumDays > 0 && !e.Source.Created.IsZero() {
			remaining := time.Duration(minimumDays)*24*time.Hour - now.Sub(e.Source.Created)
			if remaining > 0 {
				// Regional list pricing is used only to bound the
				// early-deletion portion. Bucket location can change it.
				earlyDelete += gib * remaining.Hours() / (24 * 30) * storageRate
			}
		}
	}
	gib := float64(bytes) / (1 << 30)
	classA := float64(len(entries)) * 0.05 / 1000
	classB := float64(len(entries)*3) * 0.05 / 1000
	items := []CostItem{
		{Name: "Data retrieval", Units: gib, USDMin: retrieval, USDMax: retrieval, Details: "listed storage class estimate: Standard $0, Nearline $0.01, Coldline $0.02, Archive $0.05 per GiB"},
		{Name: "Class A operations", Units: float64(len(entries)), USDMin: classA, USDMax: classA, Details: "$0.05/1,000 operations; copy/rewrite estimate"},
		{Name: "Class B operations", Units: float64(len(entries) * 3), USDMin: classB, USDMax: classB, Details: "$0.05/1,000 operations; verification/stat estimate"},
		{Name: "Early deletion", Units: gib, USDMin: 0, USDMax: earlyDelete, Details: "up to the remaining Nearline 30-day, Coldline 90-day, or Archive 365-day minimum; object age based"},
	}
	return CostEstimate{Currency: "USD", USDMin: retrieval + classA + classB, USDMax: retrieval + classA + classB + earlyDelete, Breakdown: items, PricingAsOf: "2026-08-01", Estimate: true, Warnings: []string{
		"This is an estimate, not a bill; region, free tier, taxes, and negotiated pricing are not included.",
		"Soft delete is enabled: retained source generations may continue to incur storage charges; retention duration is not available in this snapshot and is not priced here.",
	}}
}

func storageClassRates(storageClass string) (minimumDays int, retrievalPerGiB, storagePerGiBMonth float64) {
	switch strings.ToUpper(strings.TrimSpace(storageClass)) {
	case "NEARLINE":
		return 30, 0.01, 0.01
	case "COLDLINE":
		return 90, 0.02, 0.004
	case "ARCHIVE":
		return 365, 0.05, 0.0012
	default:
		return 0, 0, 0
	}
}

func EstimateEntry(entry Entry) CostEstimate {
	if !entry.Actionable {
		return CostEstimate{Currency: "USD", PricingAsOf: "2026-08-01", Estimate: true}
	}
	return estimateCost([]PlanEntry{{LogicPath: entry.LogicPath, Source: entry.Object, TargetKey: entry.TargetKey, Shared: entry.Status == SharedObject}})
}
