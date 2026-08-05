package drift

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type conflictState struct {
	db.DriftStateStore
	mu      sync.Mutex
	updates int
	failOn  int
}

type restoreCountingState struct {
	db.DriftStateStore
	restores int
}

func (s *restoreCountingState) RestoreDriftAction(ctx context.Context, id string) error {
	s.restores++
	return s.DriftStateStore.RestoreDriftAction(ctx, id)
}

func (s *conflictState) UpdateDriftAction(ctx context.Context, record db.DriftActionRecord, expected int64) (db.DriftActionRecord, error) {
	s.mu.Lock()
	s.updates++
	fail := s.updates == s.failOn
	s.mu.Unlock()
	if fail {
		return db.DriftActionRecord{}, db.ErrDriftStateConflict
	}
	return s.DriftStateStore.UpdateDriftAction(ctx, record, expected)
}

type faultObjects struct {
	mu             sync.Mutex
	objects        map[string]blob.DriftObject
	failCopy       int
	failTargetStat int
}

func (f *faultObjects) ListDriftObjects(context.Context) ([]blob.DriftObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]blob.DriftObject, 0, len(f.objects))
	for _, object := range f.objects {
		result = append(result, object)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func (f *faultObjects) StatDriftObject(_ context.Context, name string) (blob.DriftObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "docs/report.txt" && f.failTargetStat > 0 {
		f.failTargetStat--
		return blob.DriftObject{}, errors.New("injected target stat failure")
	}
	o, ok := f.objects[name]
	if !ok {
		return blob.DriftObject{}, blob.ErrDriftObjectNotFound
	}
	return o, nil
}
func (f *faultObjects) CopyDriftObject(_ context.Context, source string, generation int64, target string, metadata map[string]string) (blob.DriftObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCopy > 0 {
		f.failCopy--
		return blob.DriftObject{}, errors.New("injected copy failure")
	}
	if _, ok := f.objects[target]; ok {
		return blob.DriftObject{}, blob.ErrDriftTargetExists
	}
	sourceObject := f.objects[source]
	if sourceObject.Generation != generation {
		return blob.DriftObject{}, blob.ErrDriftPrecondition
	}
	targetObject := sourceObject
	targetObject.Name, targetObject.Generation, targetObject.Metadata = target, generation+100, metadata
	f.objects[target] = targetObject
	return targetObject, nil
}
func (f *faultObjects) DeleteDriftObject(_ context.Context, name string, generation int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if current, ok := f.objects[name]; ok && current.Generation != generation {
		return blob.ErrDriftPrecondition
	}
	delete(f.objects, name)
	return nil
}

func newActionFixture(t *testing.T, failCopy, failStat int) (*Service, db.Store, *faultObjects, Plan, Action) {
	t.Helper()
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := metadata.UpsertFile(ctx, "docs/report.txt", "legacy-uuid.txt", 7); err != nil {
		t.Fatal(err)
	}
	state, err := db.AsDriftStateStore(metadata)
	if err != nil {
		t.Fatal(err)
	}
	objects := &faultObjects{objects: map[string]blob.DriftObject{
		"legacy-uuid.txt": {Name: "legacy-uuid.txt", Size: 7, Generation: 11, CRC32C: "checksum", StorageClass: "ARCHIVE", Created: time.Now().Add(-400 * 24 * time.Hour), Metadata: map[string]string{"custom": "preserved"}},
	}, failCopy: failCopy, failTargetStat: failStat}
	service := NewForTest(metadata, objects, state, func(string) (string, error) { return "docs/report.txt", nil })
	if _, err := service.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreatePlan(ctx, []string{"docs/report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	action, err := service.CreateAction(ctx, plan.ID, "retry-key")
	if err != nil {
		t.Fatal(err)
	}
	return service, metadata, objects, plan, action
}

func TestActionRetriesAfterInjectedCopyFailure(t *testing.T) {
	service, metadata, objects, _, action := newActionFixture(t, 1, 0)
	ctx := context.Background()
	failed, err := service.ResumeAction(ctx, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.Checkpoint != CheckpointPending {
		t.Fatalf("first run = status %s checkpoint %s", failed.Status, failed.Checkpoint)
	}
	completed, err := service.ResumeAction(ctx, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" || completed.Checkpoint != CheckpointCompleted {
		t.Fatalf("retry = status %s checkpoint %s error %s", completed.Status, completed.Checkpoint, completed.Error)
	}
	record, found, err := metadata.Find(ctx, "docs/report.txt")
	if err != nil || !found || record.PhysicalHash != "docs/report.txt" {
		t.Fatalf("metadata = %+v found=%v err=%v", record, found, err)
	}
	if _, ok := objects.objects["legacy-uuid.txt"]; ok {
		t.Fatal("source object was not deleted")
	}
	if got := objects.objects["docs/report.txt"].Metadata["custom"]; got != "preserved" {
		t.Fatalf("copied custom metadata = %q, want preserved", got)
	}
}

func TestActionResumesFromCopiedCheckpointAfterVerificationFailure(t *testing.T) {
	service, _, _, _, action := newActionFixture(t, 0, 1)
	ctx := context.Background()
	failed, err := service.ResumeAction(ctx, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.Checkpoint != CheckpointCopied {
		t.Fatalf("first run = status %s checkpoint %s", failed.Status, failed.Checkpoint)
	}
	completed, err := service.ResumeAction(ctx, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("retry status = %s error=%s", completed.Status, completed.Error)
	}
}

func TestActionDeletesSourceWhenMetadataUpdateCommittedBeforeCheckpoint(t *testing.T) {
	service, metadata, objects, plan, action := newActionFixture(t, 0, 0)
	ctx := context.Background()
	entry := plan.Entries[0]
	target, err := objects.CopyDriftObject(ctx, entry.Source.Name, entry.Source.Generation, entry.TargetKey, map[string]string{"vfs-link-drift-action": action.ID})
	if err != nil {
		t.Fatal(err)
	}
	expected := entry.Source.Name
	if _, matched, err := metadata.ReplaceFileConditional(ctx, entry.LogicPath, entry.TargetKey, entry.Source.Size, &expected, false); err != nil || !matched {
		t.Fatalf("simulate metadata commit: matched=%v err=%v", matched, err)
	}
	action.Status, action.Checkpoint, action.Target = "failed", CheckpointVerified, &target
	action, err = service.saveAction(ctx, action)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.ResumeAction(ctx, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("resume status = %s error=%s", completed.Status, completed.Error)
	}
	if _, ok := objects.objects[entry.Source.Name]; ok {
		t.Fatal("source remained after crash-resume reconciliation")
	}
}

func TestActionDeletesSharedSourceAfterAllReferencesBranch(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"docs/a.txt", "docs/b.txt"} {
		if err := metadata.UpsertFile(ctx, path, "shared-uuid.txt", 7); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := db.AsDriftStateStore(metadata)
	objects := &faultObjects{objects: map[string]blob.DriftObject{
		"shared-uuid.txt": {Name: "shared-uuid.txt", Size: 7, Generation: 22, CRC32C: "checksum", StorageClass: "ARCHIVE"},
	}}
	service := NewForTest(metadata, objects, state, func(logicPath string) (string, error) {
		return "docs/" + logicPath[len("docs/"):], nil
	})
	if _, err := service.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreatePlan(ctx, []string{"docs/a.txt", "docs/b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	action, err := service.CreateAction(ctx, plan.ID, "shared-all")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.ResumeAction(ctx, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("status = %s error=%s", completed.Status, completed.Error)
	}
	if _, ok := objects.objects["shared-uuid.txt"]; ok {
		t.Fatal("shared source remained after all references branched")
	}
	for _, target := range []string{"docs/a.txt", "docs/b.txt"} {
		if _, ok := objects.objects[target]; !ok {
			t.Fatalf("target %s was not copied", target)
		}
	}
}

func TestActionKeepsSharedSourceWhileAnUnselectedReferenceRemains(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"docs/a.txt", "docs/b.txt"} {
		if err := metadata.UpsertFile(ctx, path, "shared-uuid.txt", 7); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := db.AsDriftStateStore(metadata)
	objects := &faultObjects{objects: map[string]blob.DriftObject{
		"shared-uuid.txt": {Name: "shared-uuid.txt", Size: 7, Generation: 22, CRC32C: "checksum", StorageClass: "ARCHIVE"},
	}}
	service := NewForTest(metadata, objects, state, func(logicPath string) (string, error) {
		return "docs/" + logicPath[len("docs/"):], nil
	})
	if _, err := service.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := service.CreatePlan(ctx, []string{"docs/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	action, err := service.CreateAction(ctx, plan.ID, "shared-partial")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.ResumeAction(ctx, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("status = %s error=%s", completed.Status, completed.Error)
	}
	if _, ok := objects.objects["shared-uuid.txt"]; !ok {
		t.Fatal("shared source was deleted while an unselected reference remained")
	}
	record, found, err := metadata.Find(ctx, "docs/b.txt")
	if err != nil || !found || record.PhysicalHash != "shared-uuid.txt" {
		t.Fatalf("unselected metadata = %+v found=%v err=%v", record, found, err)
	}
}

func TestActionRecoversWhenCheckpointCASConflicts(t *testing.T) {
	service, metadata, objects, _, action := newActionFixture(t, 0, 0)
	base, err := db.AsDriftStateStore(metadata)
	if err != nil {
		t.Fatal(err)
	}
	// Update 1 claims the lease; update 2 persists the copied checkpoint.
	// Simulate another instance winning that CAS after the object copy.
	service.state = &conflictState{DriftStateStore: base, failOn: 2}
	first, err := service.ResumeAction(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "failed" || first.Checkpoint != CheckpointCopied {
		t.Fatalf("conflicted run = status %s checkpoint %s error=%s", first.Status, first.Checkpoint, first.Error)
	}
	if _, ok := objects.objects["docs/report.txt"]; !ok {
		t.Fatal("copy did not commit before injected checkpoint conflict")
	}
	completed, err := service.ResumeAction(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("retry status = %s checkpoint=%s error=%s", completed.Status, completed.Checkpoint, completed.Error)
	}
}

func TestFailedActionIsTerminalUntilExplicitRetry(t *testing.T) {
	now := time.Now().UTC()
	if (Action{Status: "failed"}).NeedsKick(now) {
		t.Fatal("failed action must not be kicked by list or GET")
	}
	if !(Action{Status: "pending"}).NeedsKick(now) {
		t.Fatal("pending action should be kicked")
	}
	expired := now.Add(-time.Second)
	future := now.Add(time.Second)
	if !(Action{Status: "running", LeaseUntil: &expired}).NeedsKick(now) {
		t.Fatal("expired running action should be kicked")
	}
	if (Action{Status: "running", LeaseUntil: &future}).NeedsKick(now) {
		t.Fatal("leased running action must not be kicked")
	}
}

func TestDismissActionRequiresTerminalStatusAndHidesCompletedAction(t *testing.T) {
	service, _, _, _, action := newActionFixture(t, 0, 0)
	ctx := context.Background()
	if found, err := service.DismissAction(ctx, action.ID); !found || !errors.Is(err, ErrActionNotTerminal) {
		t.Fatalf("dismiss pending = found %t, error %v", found, err)
	}
	completed, err := service.ResumeAction(ctx, action.ID)
	if err != nil || completed.Status != "completed" {
		t.Fatalf("ResumeAction = status %q, error %v", completed.Status, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if found, err := service.DismissAction(ctx, action.ID); err != nil || !found {
			t.Fatalf("dismiss terminal attempt %d = found %t, error %v", attempt+1, found, err)
		}
	}
	actions, err := service.ListActions(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 0 {
		t.Fatalf("visible actions = %+v, want none", actions)
	}
}

func TestExplicitFailedActionRetryRestoresDismissedAction(t *testing.T) {
	service, metadata, _, plan, action := newActionFixture(t, 1, 0)
	ctx := context.Background()
	failed, err := service.ResumeAction(ctx, action.ID)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("ResumeAction = status %q, error %v", failed.Status, err)
	}
	if found, err := service.DismissAction(ctx, action.ID); err != nil || !found {
		t.Fatalf("DismissAction = found %t, error %v", found, err)
	}
	actions, err := service.ListActions(ctx, 0, 100)
	if err != nil || len(actions) != 0 {
		t.Fatalf("actions before retry = %+v, error %v", actions, err)
	}
	base, err := db.AsDriftStateStore(metadata)
	if err != nil {
		t.Fatal(err)
	}
	counting := &restoreCountingState{DriftStateStore: base}
	service.state = counting
	retried, err := service.CreateAction(ctx, plan.ID, "retry-key")
	if err != nil || retried.ID != action.ID || retried.Status != "failed" {
		t.Fatalf("CreateAction retry = %+v, error %v", retried, err)
	}
	actions, err = service.ListActions(ctx, 0, 100)
	if err != nil || len(actions) != 1 || actions[0].ID != action.ID {
		t.Fatalf("actions after retry = %+v, error %v", actions, err)
	}
	if _, err := service.ResumeAction(ctx, retried.ID); err != nil {
		t.Fatal(err)
	}
	if counting.restores < 2 {
		t.Fatalf("RestoreDriftAction calls = %d, want pre-retry and post-running restore", counting.restores)
	}
}
