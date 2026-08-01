package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	driftdomain "github.com/twkevinzhang/vfs-link/apps/file-server/internal/drift"
)

func TestDriftDefaultIsReadOnlyAndMissingSnapshotDoesNotTriggerScan(t *testing.T) {
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(metadata, objects, nil, "", "").Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/drift", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body driftSnapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.Enabled || !body.ReadOnly || body.SnapshotStatus != "missing" {
		t.Fatalf("unexpected response: %+v", body)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/drift/plans", nil)
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusForbidden {
		t.Fatalf("POST status = %d, want 403", postResponse.Code)
	}
}

func TestDriftActionsRejectNonGCSStorageEvenWhenEnabled(t *testing.T) {
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(metadata, objects, nil, "", "").SetDriftEnabled(true).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/drift/plans", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("POST status = %d, want 501; body=%s", response.Code, response.Body.String())
	}
}

func TestDriftSnapshotResponseSerializesAuditablePricingDetails(t *testing.T) {
	estimate := driftdomain.EstimateEntries([]driftdomain.Entry{{
		LogicPath:  "AHR/comic/example.7z",
		TargetKey:  "AHR/comic/example.7z",
		Size:       3 * 1024 * 1024,
		Status:     driftdomain.Drifted,
		Actionable: true,
		Object: blob.DriftObject{
			StorageClass: "ARCHIVE",
		},
	}})
	response := driftSnapshotResponse{
		Available:      true,
		StorageDriver:  "gcs",
		SnapshotStatus: "ready",
		Summary: driftSummaryResponse{
			Total:            1,
			Drifted:          1,
			EstimatedCostMin: estimate.USDMin,
			EstimatedCostMax: estimate.USDMax,
			CostBreakdown:    estimate.Breakdown,
			CostFormula:      estimate.Formula,
			Warnings:         estimate.Warnings,
		},
		PricingAsOf:    estimate.PricingAsOf,
		PricingModel:   estimate.PricingModel,
		PricingSources: estimate.Sources,
	}

	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Summary struct {
			CostBreakdown []driftdomain.CostItem  `json:"costBreakdown"`
			CostFormula   driftdomain.CostFormula `json:"costFormula"`
		} `json:"summary"`
		PricingAsOf    string                      `json:"pricingAsOf"`
		PricingModel   string                      `json:"pricingModel"`
		PricingSources []driftdomain.PricingSource `json:"pricingSources"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Summary.CostBreakdown) == 0 {
		t.Fatal("costBreakdown is empty")
	}
	if decoded.Summary.CostFormula.Minimum == "" || decoded.Summary.CostFormula.Maximum == "" {
		t.Fatalf("costFormula = %+v", decoded.Summary.CostFormula)
	}
	if decoded.PricingAsOf != driftdomain.PricingAsOf || decoded.PricingModel == "" {
		t.Fatalf("pricing metadata = %q %q", decoded.PricingAsOf, decoded.PricingModel)
	}
	if len(decoded.PricingSources) != 2 || decoded.PricingSources[0].URL == "" {
		t.Fatalf("pricingSources = %+v", decoded.PricingSources)
	}
}
