package api

import (
	"encoding/json"
	"errors"
	"net/http"
	pathpkg "path"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type pathsRequest struct {
	Paths       []string `json:"paths"`
	Destination string   `json:"destination,omitempty"`
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

func decodeBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
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
	records, err := s.files.Move(r.Context(), request.Paths, request.Destination)
	if err != nil {
		writeFileOperationError(w, err)
		return
	}
	writeJSON(w, entriesResponse{Entries: recordsToEntries(records), GeneratedAt: time.Now().Format(time.RFC3339)})
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
	records, err := s.files.Trash(r.Context(), request.Paths)
	if err != nil {
		writeFileOperationError(w, err)
		return
	}
	writeJSON(w, entriesResponse{Entries: recordsToEntries(records), GeneratedAt: time.Now().Format(time.RFC3339)})
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
	writeJSON(w, entriesResponse{Entries: recordsToEntries(records), GeneratedAt: time.Now().Format(time.RFC3339)})
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
	records, err := s.files.Restore(r.Context(), request.TrashIDs)
	if err != nil {
		writeFileOperationError(w, err)
		return
	}
	writeJSON(w, entriesResponse{Entries: recordsToEntries(records), GeneratedAt: time.Now().Format(time.RFC3339)})
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
	deleted, err := s.files.DeletePermanently(r.Context(), request.TrashIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, deletedResponse{Deleted: deleted})
}
func (s *Server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	deleted, err := s.files.DeletePermanently(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, deletedResponse{Deleted: deleted})
}
