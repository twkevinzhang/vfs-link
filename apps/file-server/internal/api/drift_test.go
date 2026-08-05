package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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
	handler := New(metadata, objects, objects, nil, "", "").Handler()
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
	handler := New(metadata, objects, objects, nil, "", "").SetDriftEnabled(true).Handler()
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

func TestDriftActionListAndDismissAPI(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.AsDriftStateStore(metadata)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	plan := driftdomain.Plan{ID: "plan-api", Fingerprint: "fingerprint-api", Entries: []driftdomain.PlanEntry{{LogicPath: "tiny.txt"}}, CreatedAt: now}
	planPayload, _ := json.Marshal(plan)
	if _, err := state.CreateDriftPlan(ctx, db.DriftPlanRecord{ID: plan.ID, Fingerprint: plan.Fingerprint, Payload: planPayload, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	createAction := func(id, status string, updatedAt time.Time) {
		t.Helper()
		action := driftdomain.Action{ID: id, PlanID: plan.ID, IdempotencyKey: "key-" + id, Status: status, Checkpoint: status, CreatedAt: updatedAt, UpdatedAt: updatedAt}
		if status == "running" {
			lease := time.Now().UTC().Add(time.Hour)
			action.LeaseUntil = &lease
		}
		payload, _ := json.Marshal(action)
		if _, err := state.CreateDriftAction(ctx, db.DriftActionRecord{ID: id, PlanID: plan.ID, IdempotencyKey: action.IdempotencyKey, Status: status, Checkpoint: status, Payload: payload, CreatedAt: updatedAt, UpdatedAt: updatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	createAction("action-completed", "completed", now.Add(time.Minute))
	createAction("action-running", "running", now)
	service := driftdomain.NewForTest(metadata, objects, state, func(path string) (string, error) { return path, nil })
	handler := (&Server{drift: service}).Handler()

	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/api/drift/actions?limit=1&offset=0", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var page driftActionsResponse
	if err := json.Unmarshal(listResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Actions) != 1 || page.Actions[0].ID != "action-completed" {
		t.Fatalf("page = %+v", page.Actions)
	}
	allResponse := httptest.NewRecorder()
	handler.ServeHTTP(allResponse, httptest.NewRequest(http.MethodGet, "/api/drift/actions?all=true", nil))
	if allResponse.Code != http.StatusOK {
		t.Fatalf("all status = %d body=%s", allResponse.Code, allResponse.Body.String())
	}
	page = driftActionsResponse{}
	if err := json.Unmarshal(allResponse.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Actions) != 2 {
		t.Fatalf("all actions = %+v, want two", page.Actions)
	}
	for _, target := range []string{"/api/drift/actions?all=true&limit=10", "/api/drift/actions?all=true&offset=0", "/api/drift/actions?all=maybe"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400", target, response.Code)
		}
	}

	nonTerminal := httptest.NewRecorder()
	handler.ServeHTTP(nonTerminal, httptest.NewRequest(http.MethodDelete, "/api/drift/actions/action-running", nil))
	if nonTerminal.Code != http.StatusConflict {
		t.Fatalf("dismiss running status = %d, want 409", nonTerminal.Code)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/drift/actions/action-completed", nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("dismiss completed attempt %d status = %d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}

	remaining := httptest.NewRecorder()
	handler.ServeHTTP(remaining, httptest.NewRequest(http.MethodGet, "/api/drift/actions?limit=100&offset=0", nil))
	if remaining.Code != http.StatusOK {
		t.Fatalf("remaining status = %d body=%s", remaining.Code, remaining.Body.String())
	}
	page = driftActionsResponse{}
	if err := json.Unmarshal(remaining.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Actions) != 1 || page.Actions[0].ID != "action-running" {
		t.Fatalf("remaining = %+v", page.Actions)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "/api/drift/actions/missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("dismiss missing status = %d, want 404", missing.Code)
	}
	badPage := httptest.NewRecorder()
	handler.ServeHTTP(badPage, httptest.NewRequest(http.MethodGet, "/api/drift/actions?limit=501", nil))
	if badPage.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, want 400", badPage.Code)
	}
}

func TestDriftScanAPIStartsAndRestoresServerJob(t *testing.T) {
	ctx := context.Background()
	metadata, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	if err := metadata.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.AsDriftStateStore(metadata)
	if err != nil {
		t.Fatal(err)
	}
	service := driftdomain.NewForTest(metadata, objects, state, func(path string) (string, error) { return path, nil })
	handler := (&Server{drift: service}).Handler()

	empty := httptest.NewRecorder()
	handler.ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/api/drift/scans/current", nil))
	if empty.Code != http.StatusOK || empty.Body.String() != "{\"scan\":null}\n" {
		t.Fatalf("empty current scan = %d %s", empty.Code, empty.Body.String())
	}

	started := httptest.NewRecorder()
	handler.ServeHTTP(started, httptest.NewRequest(http.MethodPost, "/api/drift/scans", nil))
	if started.Code != http.StatusAccepted {
		t.Fatalf("start scan = %d %s", started.Code, started.Body.String())
	}
	var initial driftScanResponse
	if err := json.Unmarshal(started.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.ID == "" || (initial.Status != "pending" && initial.Status != "running") {
		t.Fatalf("initial scan = %+v", initial)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		current := httptest.NewRecorder()
		handler.ServeHTTP(current, httptest.NewRequest(http.MethodGet, "/api/drift/scans/current", nil))
		if current.Code != http.StatusOK {
			t.Fatalf("current scan = %d %s", current.Code, current.Body.String())
		}
		var body driftCurrentScanResponse
		if err := json.Unmarshal(current.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Scan != nil && body.Scan.Status == "completed" {
			if body.Scan.ID != initial.ID || body.Scan.Phase != driftdomain.ScanPhaseComplete {
				t.Fatalf("completed scan = %+v", body.Scan)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not complete; last response %s", current.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
