package drift

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type failingScanObjects struct {
	*faultObjects
}

func (f *failingScanObjects) ListDriftObjects(context.Context) ([]blob.DriftObject, error) {
	return nil, errors.New("injected object listing failure")
}

func TestScanPersistsAcrossServiceInstancesAndCompletes(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.AsDriftStateStore(metadata)
	if err != nil {
		t.Fatal(err)
	}
	first := NewForTest(metadata, objects, state, func(path string) (string, error) { return path, nil })
	scan, created, err := first.StartScan(ctx)
	if err != nil || !created {
		t.Fatalf("StartScan() = %+v, created %t, error %v", scan, created, err)
	}
	second := NewForTest(metadata, objects, state, func(path string) (string, error) { return path, nil })

	deadline := time.Now().Add(5 * time.Second)
	for {
		persisted, found, err := second.GetScan(ctx)
		if err != nil || !found {
			t.Fatalf("GetScan() = found %t, error %v", found, err)
		}
		if persisted.Status == "completed" {
			if persisted.ID != scan.ID || persisted.Phase != ScanPhaseComplete || persisted.CompletedAt == nil {
				t.Fatalf("completed scan = %+v", persisted)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not complete: %+v", persisted)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := second.Snapshot(ctx); err != nil {
		t.Fatalf("persisted snapshot: %v", err)
	}
}

func TestConcurrentScanStartsReturnSameJob(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := db.AsDriftStateStore(metadata)
	now := time.Now().UTC()
	running := Scan{ID: "scan-existing", Status: "running", Phase: ScanPhaseObjects, CreatedAt: now, UpdatedAt: now}
	lease := now.Add(time.Hour)
	running.LeaseUntil = &lease
	payload, _ := json.Marshal(running)
	if _, created, err := state.StartDriftScan(ctx, db.DriftScanRecord{ID: running.ID, Status: running.Status, Phase: running.Phase, Payload: payload, CreatedAt: now, UpdatedAt: now}); err != nil || !created {
		t.Fatalf("seed running scan = created %t, error %v", created, err)
	}
	objects, _ := blob.NewLocal(t.TempDir())
	service := NewForTest(metadata, objects, state, func(path string) (string, error) { return path, nil })
	got, created, err := service.StartScan(ctx)
	if err != nil || created || got.ID != running.ID {
		t.Fatalf("deduplicated StartScan() = %+v, created %t, error %v", got, created, err)
	}
}

func TestFailedScanIsRetainedAndRetryCreatesNewJob(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := db.AsDriftStateStore(metadata)
	objects := &failingScanObjects{faultObjects: &faultObjects{objects: map[string]blob.DriftObject{}}}
	service := NewForTest(metadata, objects, state, func(path string) (string, error) { return path, nil })
	first, created, err := service.StartScan(ctx)
	if err != nil || !created {
		t.Fatalf("first StartScan() = %+v, created %t, error %v", first, created, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		first, _, err = service.GetScan(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if first.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not fail: %+v", first)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.Phase != ScanPhaseFailed || first.Error == "" || first.CompletedAt == nil {
		t.Fatalf("failed scan = %+v", first)
	}

	retry, created, err := service.StartScan(ctx)
	if err != nil || !created || retry.ID == first.ID {
		t.Fatalf("retry StartScan() = %+v, created %t, error %v", retry, created, err)
	}
}

func TestExpiredRunningScanResumesWithSameID(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := db.AsDriftStateStore(metadata)
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	scan := Scan{ID: "scan-expired", Status: "running", Phase: ScanPhaseObjects, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), LeaseUntil: &expired}
	payload, _ := json.Marshal(scan)
	record, created, err := state.StartDriftScan(ctx, db.DriftScanRecord{ID: scan.ID, Status: scan.Status, Phase: scan.Phase, Payload: payload, CreatedAt: scan.CreatedAt, UpdatedAt: scan.UpdatedAt})
	if err != nil || !created {
		t.Fatalf("seed expired scan = created %t, error %v", created, err)
	}
	scan.Version = record.Version
	objects, _ := blob.NewLocal(t.TempDir())
	service := NewForTest(metadata, objects, state, func(path string) (string, error) { return path, nil })
	resumed, err := service.ResumeScan(ctx, scan.ID)
	if err != nil || resumed.ID != scan.ID || resumed.Status != "completed" {
		t.Fatalf("ResumeScan() = %+v, error %v", resumed, err)
	}
}
