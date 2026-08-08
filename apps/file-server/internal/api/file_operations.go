package api

import (
	"errors"
	"net/http"
	pathpkg "path"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type pathsRequest struct {
	Paths       []string `json:"paths"`
	Destination string   `json:"destination,omitempty"`
}
type renameRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}
type trashIDsRequest struct {
	TrashIDs []string `json:"trashIds"`
}
type entriesResponse struct {
	Entries     []Entry `json:"entries"`
	GeneratedAt string  `json:"generatedAt"`
}
type deletedResponse struct {
	Deleted int64 `json:"deleted"`
}
type operationResponse struct {
	OperationID string  `json:"operationId"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	Progress    int     `json:"progress"`
	Total       int     `json:"total"`
	Deleted     int64   `json:"deleted,omitempty"`
	Error       string  `json:"error,omitempty"`
	Entries     []Entry `json:"entries,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

func recordsToEntries(records []db.FileRecord) []Entry {
	result := make([]Entry, 0, len(records))
	for _, record := range records {
		result = append(result, Entry{Name: pathpkg.Base(record.LogicPath), Path: record.LogicPath, Kind: map[bool]string{true: "directory", false: "file"}[record.IsDirectory], Size: record.Size, UpdatedAt: record.UpdatedAt, PhysicalHash: record.PhysicalHash, TrashID: record.TrashID, TrashedAt: record.TrashedAt})
	}
	return result
}
func writeFileOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, db.ErrMetadataRateLimit):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, db.ErrPathConflict), errors.Is(err, db.ErrInvalidMove), errors.Is(err, db.ErrTrashBusy):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) handleMoveFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request pathsRequest
	if decodeBody(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	result, err := s.files.Move(r.Context(), request.Paths, request.Destination)
	if err != nil {
		writeFileOperationError(w, err)
		return
	}
	if result.Operation != nil {
		writeAcceptedOperation(w, *result.Operation)
		return
	}
	writeJSON(w, entriesResponse{Entries: recordsToEntries(result.Records), GeneratedAt: time.Now().Format(time.RFC3339)})
}

func (s *Server) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request renameRequest
	if decodeBody(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	result, err := s.files.Rename(r.Context(), request.Path, request.Name)
	if err != nil {
		writeFileOperationError(w, err)
		return
	}
	if result.Operation != nil {
		writeAcceptedOperation(w, *result.Operation)
		return
	}
	writeJSON(w, entriesResponse{Entries: recordsToEntries(result.Records), GeneratedAt: time.Now().Format(time.RFC3339)})
}

func (s *Server) handleOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/operations/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	operation, found, err := s.files.Operation(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	writeJSON(w, operationToResponse(operation))
}

func operationToResponse(operation db.OperationRecord) operationResponse {
	return operationResponse{
		OperationID: operation.ID,
		Type:        operation.Type,
		Status:      operation.Status,
		Progress:    operation.Progress,
		Total:       operation.Total,
		Deleted:     operation.Deleted,
		Error:       operation.Error,
		Entries:     recordsToEntries(operation.Result),
		CreatedAt:   operation.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   operation.UpdatedAt.Format(time.RFC3339),
	}
}
func (s *Server) handleTrashFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request pathsRequest
	if decodeBody(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	result, err := s.files.Trash(r.Context(), request.Paths)
	if err != nil {
		writeFileOperationError(w, err)
		return
	}
	if result.Operation != nil {
		writeAcceptedOperation(w, *result.Operation)
		return
	}
	writeJSON(w, entriesResponse{Entries: recordsToEntries(result.Records), GeneratedAt: time.Now().Format(time.RFC3339)})
}
func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	records, err := s.files.ListTrash(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries, err := s.entriesWithThumbnails(r.Context(), records)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, entriesResponse{Entries: entries, GeneratedAt: time.Now().Format(time.RFC3339)})
}
func (s *Server) handleRestoreTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request trashIDsRequest
	if decodeBody(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	result, err := s.files.Restore(r.Context(), request.TrashIDs)
	if err != nil {
		writeFileOperationError(w, err)
		return
	}
	if result.Operation != nil {
		writeAcceptedOperation(w, *result.Operation)
		return
	}
	writeJSON(w, entriesResponse{Entries: recordsToEntries(result.Records), GeneratedAt: time.Now().Format(time.RFC3339)})
}
func (s *Server) handleDeleteTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request trashIDsRequest
	if decodeBody(r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(request.TrashIDs) == 0 {
		writeError(w, http.StatusBadRequest, "at least one trash id is required")
		return
	}
	result, err := s.files.DeletePermanently(r.Context(), request.TrashIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if result.Operation != nil {
		writeAcceptedOperation(w, *result.Operation)
		return
	}
	writeJSON(w, deletedResponse{Deleted: result.Deleted})
}
func (s *Server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	result, err := s.files.DeletePermanently(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if result.Operation != nil {
		writeAcceptedOperation(w, *result.Operation)
		return
	}
	writeJSON(w, deletedResponse{Deleted: result.Deleted})
}

func writeAcceptedOperation(w http.ResponseWriter, operation db.OperationRecord) {
	w.Header().Set("Location", "/api/operations/"+operation.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, operationToResponse(operation))
}
