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
	Name         string  `json:"name"`
	StorageClass string  `json:"storageClass,omitempty"`
	Units        float64 `json:"units"`
	UnitLabel    string  `json:"unitLabel"`
	Rate         float64 `json:"rate"`
	RateUnit     string  `json:"rateUnit"`
	Formula      string  `json:"formula"`
	USDMin       float64 `json:"usdMin"`
	USDMax       float64 `json:"usdMax"`
	Details      string  `json:"details"`
}

type CostFormula struct {
	Minimum string `json:"minimum"`
	Maximum string `json:"maximum"`
}

type PricingSource struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type CostEstimate struct {
	Currency     string          `json:"currency"`
	USDMin       float64         `json:"usdMin"`
	USDMax       float64         `json:"usdMax"`
	Breakdown    []CostItem      `json:"breakdown"`
	Formula      CostFormula     `json:"formula"`
	PricingAsOf  string          `json:"pricingAsOf"`
	PricingModel string          `json:"pricingModel"`
	Sources      []PricingSource `json:"sources"`
	Estimate     bool            `json:"estimate"`
	Warnings     []string        `json:"warnings"`
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

const PricingAsOf = "2026-08-01"

var pricingSources = []PricingSource{
	{Label: "Google Cloud Storage pricing", URL: "https://cloud.google.com/storage/pricing"},
	{Label: "Google Cloud Storage classes", URL: "https://cloud.google.com/storage/docs/storage-classes"},
}

type storageClassPricing struct {
	name               string
	minimumDays        int
	retrievalPerGiB    float64
	storagePerGiBMonth float64
	classAPerThousand  float64
	classBPerThousand  float64
}

type storageClassCostGroup struct {
	pricing             storageClassPricing
	bytes               int64
	objects             int
	earlyDeleteGiBMonth float64
}

func estimateCost(entries []PlanEntry) CostEstimate {
	groups := make(map[string]*storageClassCostGroup)
	unknownClasses := make(map[string]struct{})
	now := time.Now().UTC()
	for _, entry := range entries {
		pricing, known := pricingForStorageClass(entry.Source.StorageClass)
		if !known {
			unknownClasses[pricing.name] = struct{}{}
		}
		group := groups[pricing.name]
		if group == nil {
			group = &storageClassCostGroup{pricing: pricing}
			groups[pricing.name] = group
		}
		group.bytes += entry.Source.Size
		group.objects++
		if pricing.minimumDays == 0 || entry.Source.Created.IsZero() {
			continue
		}
		remaining := time.Duration(pricing.minimumDays)*24*time.Hour - now.Sub(entry.Source.Created)
		if remaining > 0 {
			gib := float64(entry.Source.Size) / (1 << 30)
			group.earlyDeleteGiBMonth += gib * remaining.Hours() / (24 * 30)
		}
	}

	classNames := make([]string, 0, len(groups))
	for name := range groups {
		classNames = append(classNames, name)
	}
	sort.Slice(classNames, func(i, j int) bool {
		return storageClassSortKey(classNames[i]) < storageClassSortKey(classNames[j])
	})

	items := make([]CostItem, 0, len(classNames)*4)
	var minimum, earlyDelete float64
	for _, name := range classNames {
		group := groups[name]
		gib := float64(group.bytes) / (1 << 30)
		retrieval := gib * group.pricing.retrievalPerGiB
		classA := float64(group.objects) * group.pricing.classAPerThousand / 1000
		classBUnits := float64(group.objects * 3)
		classB := classBUnits * group.pricing.classBPerThousand / 1000
		minimum += retrieval + classA + classB
		earlyDeleteForClass := group.earlyDeleteGiBMonth * group.pricing.storagePerGiBMonth
		earlyDelete += earlyDeleteForClass
		items = append(items,
			CostItem{
				Name: "Data retrieval", StorageClass: name, Units: gib, UnitLabel: "GiB",
				Rate: group.pricing.retrievalPerGiB, RateUnit: "USD/GiB",
				Formula: "stored GiB × retrieval rate", USDMin: retrieval, USDMax: retrieval,
				Details: "Retrieval applies when Cloud Storage reads, copies, moves, or rewrites non-Standard data.",
			},
			CostItem{
				Name: "Class A operations", StorageClass: name, Units: float64(group.objects), UnitLabel: "operations",
				Rate: group.pricing.classAPerThousand, RateUnit: "USD/1,000 operations",
				Formula: "object count × Class A rate ÷ 1,000", USDMin: classA, USDMax: classA,
				Details: "One copy or rewrite operation is estimated per object.",
			},
			CostItem{
				Name: "Class B operations", StorageClass: name, Units: classBUnits, UnitLabel: "operations",
				Rate: group.pricing.classBPerThousand, RateUnit: "USD/1,000 operations",
				Formula: "estimated verification operations × Class B rate ÷ 1,000", USDMin: classB, USDMax: classB,
				Details: "Three metadata or verification operations are estimated per object.",
			},
		)
		if group.pricing.minimumDays > 0 {
			items = append(items, CostItem{
				Name: "Early deletion", StorageClass: name, Units: group.earlyDeleteGiBMonth, UnitLabel: "GiB-month",
				Rate: group.pricing.storagePerGiBMonth, RateUnit: "USD/GiB-month",
				Formula: "Σ(object GiB × remaining minimum-storage days ÷ 30) × storage rate",
				USDMin:  0, USDMax: earlyDeleteForClass,
				Details: fmt.Sprintf("Upper bound based on object age and the %d-day minimum storage duration.", group.pricing.minimumDays),
			})
		}
	}

	warnings := []string{
		"This is an estimate, not a bill; free tier, taxes, and negotiated pricing are not included.",
		"Regional flat-namespace list pricing is assumed; bucket location, namespace type, Autoclass, and network topology can change the bill.",
		"Soft delete is enabled: retained source generations may continue to incur storage charges; retention duration is not available in this snapshot and is not priced here.",
	}
	if len(unknownClasses) > 0 {
		unknown := make([]string, 0, len(unknownClasses))
		for name := range unknownClasses {
			unknown = append(unknown, name)
		}
		sort.Strings(unknown)
		warnings = append(warnings, "Unknown storage classes use conservative Archive operation rates and no retrieval or early-deletion rate: "+strings.Join(unknown, ", ")+".")
	}
	return CostEstimate{
		Currency: "USD", USDMin: minimum, USDMax: minimum + earlyDelete, Breakdown: items,
		Formula: CostFormula{
			Minimum: "Data retrieval + Class A operations + Class B operations",
			Maximum: "Minimum estimate + early deletion upper bound",
		},
		PricingAsOf:  PricingAsOf,
		PricingModel: "Google Cloud Storage regional flat-namespace list pricing",
		Sources:      append([]PricingSource(nil), pricingSources...), Estimate: true, Warnings: warnings,
	}
}

func pricingForStorageClass(storageClass string) (storageClassPricing, bool) {
	name := strings.ToUpper(strings.TrimSpace(storageClass))
	switch name {
	case "STANDARD":
		return storageClassPricing{name: name, classAPerThousand: 0.005, classBPerThousand: 0.0004}, true
	case "NEARLINE":
		return storageClassPricing{name: name, minimumDays: 30, retrievalPerGiB: 0.01, storagePerGiBMonth: 0.01, classAPerThousand: 0.01, classBPerThousand: 0.001}, true
	case "COLDLINE":
		return storageClassPricing{name: name, minimumDays: 90, retrievalPerGiB: 0.02, storagePerGiBMonth: 0.004, classAPerThousand: 0.02, classBPerThousand: 0.01}, true
	case "ARCHIVE":
		return storageClassPricing{name: name, minimumDays: 365, retrievalPerGiB: 0.05, storagePerGiBMonth: 0.0012, classAPerThousand: 0.05, classBPerThousand: 0.05}, true
	default:
		if name == "" {
			name = "UNKNOWN"
		}
		return storageClassPricing{name: name, classAPerThousand: 0.05, classBPerThousand: 0.05}, false
	}
}

func storageClassSortKey(storageClass string) string {
	switch storageClass {
	case "STANDARD":
		return "0"
	case "NEARLINE":
		return "1"
	case "COLDLINE":
		return "2"
	case "ARCHIVE":
		return "3"
	default:
		return "4" + storageClass
	}
}

func storageClassRates(storageClass string) (minimumDays int, retrievalPerGiB, storagePerGiBMonth float64) {
	pricing, _ := pricingForStorageClass(storageClass)
	return pricing.minimumDays, pricing.retrievalPerGiB, pricing.storagePerGiBMonth
}

func EstimateEntries(entries []Entry) CostEstimate {
	planEntries := make([]PlanEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.Actionable {
			continue
		}
		planEntries = append(planEntries, PlanEntry{LogicPath: entry.LogicPath, Source: entry.Object, TargetKey: entry.TargetKey, Shared: entry.Status == SharedObject})
	}
	return estimateCost(planEntries)
}

func EstimateEntry(entry Entry) CostEstimate {
	if !entry.Actionable {
		return CostEstimate{Currency: "USD", PricingAsOf: PricingAsOf, Estimate: true}
	}
	return EstimateEntries([]Entry{entry})
}
