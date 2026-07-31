package drift

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/objectkey"
)

func TestCreatePlanRejectsSanitizedTargetCollisions(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for path, physical := range map[string]string{
		"docs/a:b.txt": "legacy-a",
		"docs/a?b.txt": "legacy-b",
	} {
		if err := metadata.UpsertFile(ctx, path, physical, 7); err != nil {
			t.Fatal(err)
		}
	}
	state, _ := db.AsDriftStateStore(metadata)
	objects := &faultObjects{objects: map[string]blob.DriftObject{
		"legacy-a": {Name: "legacy-a", Size: 7, Generation: 1, CRC32C: "a"},
		"legacy-b": {Name: "legacy-b", Size: 7, Generation: 2, CRC32C: "b"},
	}}
	service := NewForTest(metadata, objects, state, func(string) (string, error) {
		return "docs/a_b.txt", nil
	})
	if _, err := service.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreatePlan(ctx, []string{"docs/a:b.txt", "docs/a?b.txt"})
	if err == nil || !strings.Contains(err.Error(), "sanitized target collision") {
		t.Fatalf("CreatePlan error = %v, want sanitized target collision", err)
	}
}

func TestRefreshTreatsLegacyNFDObjectKeyAsDrifted(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	const nfdKey = "comic/は\u309aん太.zip"
	const nfcKey = "comic/ぱん太.zip"
	if err := metadata.UpsertFile(ctx, nfcKey, nfdKey, 7); err != nil {
		t.Fatal(err)
	}
	state, _ := db.AsDriftStateStore(metadata)
	objects := &faultObjects{objects: map[string]blob.DriftObject{
		nfdKey: {Name: nfdKey, Size: 7, Generation: 1},
	}}
	service := NewForTest(metadata, objects, state, objectkey.FromLogicalPath)
	snapshot, err := service.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != Drifted || snapshot.Entries[0].TargetKey != nfcKey {
		t.Fatalf("NFD snapshot = %#v", snapshot.Entries)
	}
}

func TestEstimateCostUsesListedStorageClassRetrievalRate(t *testing.T) {
	entry := func(storageClass string) PlanEntry {
		return PlanEntry{Source: blob.DriftObject{Size: 1 << 30, StorageClass: storageClass}}
	}
	standard := estimateCost([]PlanEntry{entry("STANDARD")})
	archive := estimateCost([]PlanEntry{entry("ARCHIVE")})
	standardOperations := 0.005/1000 + 3*0.0004/1000
	archiveOperations := 0.05/1000 + 3*0.05/1000
	if math.Abs(standard.USDMin-standardOperations) > 1e-12 {
		t.Fatalf("standard minimum = %f, want operations only %f", standard.USDMin, standardOperations)
	}
	if math.Abs(archive.USDMin-(archiveOperations+0.05)) > 1e-12 {
		t.Fatalf("archive minimum = %f, want retrieval plus operations", archive.USDMin)
	}
}

func TestEstimateEntriesReturnsAuditableSnapshotBreakdown(t *testing.T) {
	entries := []Entry{
		{
			LogicPath: "archive.bin", Actionable: true, Status: Drifted,
			Object: blob.DriftObject{Size: 2 << 30, StorageClass: "ARCHIVE", Created: time.Now().UTC().Add(-24 * time.Hour)},
		},
		{
			LogicPath: "aligned.bin", Actionable: false, Status: Aligned,
			Object: blob.DriftObject{Size: 100 << 30, StorageClass: "ARCHIVE"},
		},
	}
	estimate := EstimateEntries(entries)
	if estimate.USDMin <= 0 || estimate.USDMax <= estimate.USDMin {
		t.Fatalf("estimate range = %f–%f, want positive early-deletion upper bound", estimate.USDMin, estimate.USDMax)
	}
	if len(estimate.Breakdown) != 4 {
		t.Fatalf("breakdown length = %d, want four Archive calculation rows", len(estimate.Breakdown))
	}
	var minimum, maximum float64
	for _, item := range estimate.Breakdown {
		minimum += item.USDMin
		maximum += item.USDMax
		if item.StorageClass != "ARCHIVE" || item.UnitLabel == "" || item.RateUnit == "" || item.Formula == "" {
			t.Fatalf("incomplete cost item: %+v", item)
		}
	}
	if math.Abs(minimum-estimate.USDMin) > 1e-12 || math.Abs(maximum-estimate.USDMax) > 1e-12 {
		t.Fatalf("breakdown %f–%f does not reconcile total %f–%f", minimum, maximum, estimate.USDMin, estimate.USDMax)
	}
	if estimate.Formula.Minimum == "" || estimate.Formula.Maximum == "" || len(estimate.Sources) != 2 || estimate.PricingModel == "" {
		t.Fatalf("missing explanation metadata: %+v", estimate)
	}
}
