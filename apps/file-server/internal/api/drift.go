package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	driftdomain "github.com/twkevinzhang/vfs-link/apps/file-server/internal/drift"
)

const driftPricingAsOf = driftdomain.PricingAsOf

type driftItemResponse struct {
	LogicPath        string  `json:"logicPath,omitempty"`
	CurrentKey       string  `json:"currentKey"`
	TargetKey        string  `json:"targetKey,omitempty"`
	Status           string  `json:"status"`
	Size             int64   `json:"size"`
	StorageClass     string  `json:"storageClass,omitempty"`
	Generation       int64   `json:"generation,omitempty"`
	Method           string  `json:"method"`
	Actionable       bool    `json:"actionable"`
	EstimatedCostMin float64 `json:"estimatedCostUsdMin"`
	EstimatedCostMax float64 `json:"estimatedCostUsdMax"`
	Error            string  `json:"error,omitempty"`
}

type driftSummaryResponse struct {
	Total            int                     `json:"total"`
	Aligned          int                     `json:"aligned"`
	Drifted          int                     `json:"drifted"`
	Missing          int                     `json:"missing"`
	Failed           int                     `json:"failed"`
	TotalBytes       int64                   `json:"totalBytes"`
	EstimatedCostMin float64                 `json:"estimatedCostUsdMin"`
	EstimatedCostMax float64                 `json:"estimatedCostUsdMax"`
	CostBreakdown    []driftdomain.CostItem  `json:"costBreakdown"`
	CostFormula      driftdomain.CostFormula `json:"costFormula"`
	Warnings         []string                `json:"warnings"`
}

type driftPaginationResponse struct {
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
	Total   int    `json:"total"`
	Query   string `json:"query"`
	HasNext bool   `json:"hasNext"`
	HasPrev bool   `json:"hasPrev"`
}

type driftSnapshotResponse struct {
	Available      bool                        `json:"available"`
	Enabled        bool                        `json:"enabled"`
	ReadOnly       bool                        `json:"readOnly"`
	StorageDriver  string                      `json:"storageDriver"`
	SnapshotStatus string                      `json:"snapshotStatus"`
	Scanning       bool                        `json:"scanning"`
	Summary        driftSummaryResponse        `json:"summary"`
	Items          []driftItemResponse         `json:"items"`
	Pagination     driftPaginationResponse     `json:"pagination"`
	PricingAsOf    string                      `json:"pricingAsOf"`
	PricingModel   string                      `json:"pricingModel"`
	PricingSources []driftdomain.PricingSource `json:"pricingSources"`
	GeneratedAt    string                      `json:"generatedAt,omitempty"`
	Error          string                      `json:"error,omitempty"`
}

func (s *Server) driftMutationReady(w http.ResponseWriter) bool {
	if s.driftErr != nil {
		writeError(w, http.StatusNotImplemented, s.driftErr.Error())
		return false
	}
	if !s.driftEnabled {
		writeError(w, http.StatusForbidden, "drift actions are disabled")
		return false
	}
	if !strings.EqualFold(s.objects.Driver(), "gcs") {
		writeError(w, http.StatusNotImplemented, "drift actions require STORAGE_DRIVER=gcs")
		return false
	}
	return true
}

func driftMethod(entry driftdomain.Entry) string {
	if !entry.Actionable {
		return "display_only"
	}
	if entry.Status == driftdomain.SharedObject {
		return "copy_on_branch"
	}
	return "copy_verify_update_delete"
}

func driftResponseItem(entry driftdomain.Entry, gcs bool) driftItemResponse {
	cost := driftdomain.CostEstimate{}
	if gcs {
		cost = driftdomain.EstimateEntry(entry)
	}
	return driftItemResponse{
		LogicPath: entry.LogicPath, CurrentKey: entry.PhysicalHash, TargetKey: entry.TargetKey,
		Status: string(entry.Status), Size: entry.Size, StorageClass: entry.Object.StorageClass,
		Generation: entry.Object.Generation, Method: driftMethod(entry),
		Actionable:       entry.Actionable && gcs,
		EstimatedCostMin: cost.USDMin, EstimatedCostMax: cost.USDMax, Error: entry.Error,
	}
}

func (s *Server) emptyDriftResponse(status, message string) driftSnapshotResponse {
	return driftSnapshotResponse{
		Available: s.driftErr == nil, Enabled: s.driftEnabled, ReadOnly: !s.driftEnabled,
		StorageDriver: s.objects.Driver(), SnapshotStatus: status, Items: []driftItemResponse{},
		Pagination: driftPaginationResponse{Limit: 100}, PricingAsOf: driftPricingAsOf, Error: message,
	}
}

func (s *Server) handleDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.driftErr != nil {
		writeJSON(w, s.emptyDriftResponse("unavailable", s.driftErr.Error()))
		return
	}
	var snapshot driftdomain.Snapshot
	var err error
	if r.URL.Query().Get("refresh") == "true" {
		snapshot, err = s.drift.Refresh(r.Context())
	} else {
		snapshot, err = s.drift.Snapshot(r.Context())
	}
	if errors.Is(err, driftdomain.ErrNoSnapshot) {
		writeJSON(w, s.emptyDriftResponse("missing", "explicitly request refresh=true to create a snapshot"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries := snapshot.Entries
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	filtered := make([]driftdomain.Entry, 0, len(entries))
	for _, entry := range entries {
		if status != "" && string(entry.Status) != status {
			continue
		}
		if scope != "" && entry.Scope != scope {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.LogicPath), query) && !strings.Contains(strings.ToLower(entry.PhysicalHash), query) && !strings.Contains(strings.ToLower(entry.TargetKey), query) {
			continue
		}
		filtered = append(filtered, entry)
	}
	limit, offset := 100, 0
	if n, parseErr := strconv.Atoi(r.URL.Query().Get("limit")); parseErr == nil && n > 0 && n <= 500 {
		limit = n
	}
	if n, parseErr := strconv.Atoi(r.URL.Query().Get("offset")); parseErr == nil && n >= 0 {
		offset = n
	}
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := make([]driftItemResponse, 0, end-offset)
	gcs := strings.EqualFold(s.objects.Driver(), "gcs")
	for _, entry := range filtered[offset:end] {
		items = append(items, driftResponseItem(entry, gcs))
	}
	cost := driftdomain.CostEstimate{}
	if gcs {
		cost = driftdomain.EstimateEntries(snapshot.Entries)
	}
	summary := driftSummaryResponse{
		Total: len(snapshot.Entries), EstimatedCostMin: cost.USDMin, EstimatedCostMax: cost.USDMax,
		CostBreakdown: cost.Breakdown, CostFormula: cost.Formula, Warnings: cost.Warnings,
	}
	for _, entry := range snapshot.Entries {
		if entry.Actionable && gcs {
			summary.TotalBytes += entry.Size
		}
		switch entry.Status {
		case driftdomain.Aligned:
			summary.Aligned++
		case driftdomain.Drifted, driftdomain.SharedObject:
			summary.Drifted++
		case driftdomain.ObjectMissing:
			summary.Missing++
		default:
			summary.Failed++
		}
	}
	writeJSON(w, driftSnapshotResponse{
		Available: true, Enabled: s.driftEnabled, ReadOnly: !s.driftEnabled,
		StorageDriver: s.objects.Driver(), SnapshotStatus: "ready", Summary: summary, Items: items,
		Pagination:  driftPaginationResponse{Limit: limit, Offset: offset, Total: total, Query: query, HasNext: end < total, HasPrev: offset > 0},
		PricingAsOf: cost.PricingAsOf, PricingModel: cost.PricingModel, PricingSources: cost.Sources,
		GeneratedAt: snapshot.GeneratedAt.Format(time.RFC3339),
	})
}

type driftPlanResponse struct {
	PlanID           string                 `json:"planId"`
	Fingerprint      string                 `json:"fingerprint"`
	Items            []driftItemResponse    `json:"items"`
	Paths            []string               `json:"paths"`
	TotalBytes       int64                  `json:"totalBytes"`
	EstimatedCostMin float64                `json:"estimatedCostUsdMin"`
	EstimatedCostMax float64                `json:"estimatedCostUsdMax"`
	PricingAsOf      string                 `json:"pricingAsOf"`
	Method           string                 `json:"method"`
	CostBreakdown    []driftdomain.CostItem `json:"costBreakdown"`
	Warnings         []string               `json:"warnings"`
	CreatedAt        time.Time              `json:"createdAt"`
}

func planResponse(plan driftdomain.Plan) driftPlanResponse {
	response := driftPlanResponse{PlanID: plan.ID, Fingerprint: plan.Fingerprint, EstimatedCostMin: plan.Cost.USDMin, EstimatedCostMax: plan.Cost.USDMax, PricingAsOf: plan.Cost.PricingAsOf, Method: "copy_verify_update_conditional_delete", CostBreakdown: plan.Cost.Breakdown, Warnings: plan.Cost.Warnings, CreatedAt: plan.CreatedAt}
	for _, entry := range plan.Entries {
		response.Paths = append(response.Paths, entry.LogicPath)
		response.TotalBytes += entry.Source.Size
		method := "copy_verify_update_delete"
		if entry.Shared {
			method = "copy_on_branch"
		}
		response.Items = append(response.Items, driftItemResponse{LogicPath: entry.LogicPath, CurrentKey: entry.Source.Name, TargetKey: entry.TargetKey, Status: "planned", Size: entry.Source.Size, StorageClass: entry.Source.StorageClass, Generation: entry.Source.Generation, Method: method, Actionable: true})
	}
	return response
}

func (s *Server) handleDriftPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.driftMutationReady(w) {
		return
	}
	var request struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	plan, err := s.drift.CreatePlan(r.Context(), request.Paths)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeDriftJSONStatus(w, http.StatusCreated, planResponse(plan))
}

type driftActionResult struct {
	LogicPath string `json:"logicPath"`
	Status    string `json:"status"`
}
type driftActionResponse struct {
	ID             string              `json:"id"`
	PlanID         string              `json:"planId"`
	IdempotencyKey string              `json:"idempotencyKey"`
	Status         string              `json:"status"`
	Progress       int                 `json:"progress"`
	Total          int                 `json:"total"`
	Succeeded      int                 `json:"succeeded"`
	Failed         int                 `json:"failed"`
	Results        []driftActionResult `json:"results"`
	Error          string              `json:"error,omitempty"`
	CreatedAt      time.Time           `json:"createdAt"`
	UpdatedAt      time.Time           `json:"updatedAt"`
}

func (s *Server) actionResponse(r *http.Request, action driftdomain.Action) (driftActionResponse, error) {
	plan, ok, err := s.drift.GetPlan(r.Context(), action.PlanID)
	if err != nil || !ok {
		return driftActionResponse{}, err
	}
	response := driftActionResponse{ID: action.ID, PlanID: action.PlanID, IdempotencyKey: action.IdempotencyKey, Status: action.Status, Progress: action.EntryIndex, Total: len(plan.Entries), Error: action.Error, CreatedAt: action.CreatedAt, UpdatedAt: action.UpdatedAt, Results: []driftActionResult{}}
	if action.Status == "completed" {
		response.Progress, response.Succeeded = len(plan.Entries), len(plan.Entries)
	} else if action.Status == "failed" {
		response.Failed = 1
	}
	for index, entry := range plan.Entries {
		status := "pending"
		if index < response.Progress {
			status = "succeeded"
		} else if index == action.EntryIndex && action.Status == "failed" {
			status = "failed"
		}
		response.Results = append(response.Results, driftActionResult{LogicPath: entry.LogicPath, Status: status})
	}
	return response, nil
}

func (s *Server) handleDriftActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.driftMutationReady(w) {
		return
	}
	var request struct {
		PlanID         string `json:"planId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		request.IdempotencyKey = uuid.NewString()
	}
	action, err := s.drift.CreateAction(r.Context(), request.PlanID, request.IdempotencyKey)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	response, err := s.actionResponse(r, action)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeDriftJSONStatus(w, http.StatusAccepted, response)
}

func (s *Server) handleDriftAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.driftErr != nil {
		writeError(w, http.StatusNotImplemented, s.driftErr.Error())
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/drift/actions/"), "/")
	action, ok, err := s.drift.GetAction(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "drift action not found")
		return
	}
	if action.Status == "pending" || action.Status == "failed" || (action.Status == "running" && action.LeaseUntil != nil && action.LeaseUntil.Before(time.Now())) {
		s.drift.KickAction(action.ID)
	}
	response, err := s.actionResponse(r, action)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, response)
}

func writeDriftJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
