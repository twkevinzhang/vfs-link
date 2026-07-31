package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/upload"
)

type createUploadRequest struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	Overwrite   bool   `json:"overwrite"`
}

type uploadResponse struct {
	ID          string            `json:"id"`
	LogicPath   string            `json:"logicPath"`
	Size        int64             `json:"size"`
	ContentType string            `json:"contentType"`
	Status      string            `json:"status"`
	Error       string            `json:"error,omitempty"`
	Method      string            `json:"method"`
	UploadURL   string            `json:"uploadUrl"`
	Headers     map[string]string `json:"headers"`
	CompleteURL string            `json:"completeUrl"`
	StatusURL   string            `json:"statusUrl"`
	ExpiresAt   time.Time         `json:"expiresAt"`
}

func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "uploads are not configured")
		return
	}
	var request createUploadRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	session, err := s.uploads.Create(r.Context(), upload.CreateInput{
		LogicPath: request.Path, Size: request.Size, ContentType: request.ContentType, Overwrite: request.Overwrite,
		Origin: requestOrigin(r),
	})
	if err != nil {
		writeUploadError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, toUploadResponse(session, true))
}

func requestOrigin(r *http.Request) string {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return origin
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto != "http" && proto != "https" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	return proto + "://" + r.Host
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "uploads are not configured")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/uploads/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "upload not found")
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			session, err := s.uploads.Find(r.Context(), id)
			if err != nil {
				writeUploadError(w, err)
				return
			}
			writeJSON(w, toUploadResponse(session, false))
		case http.MethodDelete:
			if err := s.uploads.Cancel(r.Context(), id); err != nil {
				writeUploadError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if len(parts) == 2 && parts[1] == "content" && r.Method == http.MethodPut {
		session, err := s.uploads.Write(r.Context(), id, r.Body)
		if err != nil {
			writeUploadError(w, err)
			return
		}
		writeJSON(w, toUploadResponse(session, false))
		return
	}
	if len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost {
		session, err := s.uploads.Complete(r.Context(), id)
		if err != nil {
			writeUploadError(w, err)
			return
		}
		writeJSON(w, toUploadResponse(session, false))
		return
	}
	writeError(w, http.StatusNotFound, "upload endpoint not found")
}

func toUploadResponse(session upload.Session, includeUploadCapability bool) uploadResponse {
	uploadURL := ""
	if includeUploadCapability {
		uploadURL = session.UploadURL
	}
	if uploadURL == "" && session.Driver != "gcs" {
		uploadURL = "/api/uploads/" + session.ID + "/content"
	}
	var headers map[string]string
	if includeUploadCapability {
		headers = session.UploadHeaders
	}
	if headers == nil {
		headers = map[string]string{}
	}
	return uploadResponse{
		ID: session.ID, LogicPath: session.LogicPath, Size: session.Size, ContentType: session.ContentType,
		Status: session.Status, Error: session.Error, Method: http.MethodPut, UploadURL: uploadURL, Headers: headers,
		CompleteURL: "/api/uploads/" + session.ID + "/complete", StatusURL: "/api/uploads/" + session.ID,
		ExpiresAt: session.ExpiresAt,
	}
}

func writeUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upload.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, upload.ErrFileExists), errors.Is(err, upload.ErrConflict), errors.Is(err, upload.ErrInvalidSession):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
