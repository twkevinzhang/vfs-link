package drift

import (
	"context"
	"math"
	"strings"
	"testing"

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
	operationCost := 0.05/1000 + 3*0.05/1000
	if math.Abs(standard.USDMin-operationCost) > 1e-12 {
		t.Fatalf("standard minimum = %f, want operations only %f", standard.USDMin, operationCost)
	}
	if math.Abs(archive.USDMin-(operationCost+0.05)) > 1e-12 {
		t.Fatalf("archive minimum = %f, want retrieval plus operations", archive.USDMin)
	}
}
