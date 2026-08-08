package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/upload"
)

type createUploadRequest struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	ContentType   string `json:"contentType"`
	Overwrite     bool   `json:"overwrite"`
	TargetVersion string `json:"targetVersion,omitempty"`
}

type preflightUploadRequest struct {
	Items []preflightUploadItem `json:"items"`
}

type preflightUploadItem struct {
	ClientID string `json:"clientId"`
	Path     string `json:"path"`
}

type preflightUploadResponse struct {
	Items []upload.PreflightResult `json:"items"`
}

type uploadResponse struct {
	ID           string            `json:"id"`
	LogicPath    string            `json:"logicPath"`
	Size         int64             `json:"size"`
	UploadedSize int64             `json:"uploadedSize"`
	ContentType  string            `json:"contentType"`
	Status       string            `json:"status"`
	Error        string            `json:"error,omitempty"`
	Method       string            `json:"method"`
	UploadURL    string            `json:"uploadUrl"`
	Headers      map[string]string `json:"headers"`
	CompleteURL  string            `json:"completeUrl"`
	StatusURL    string            `json:"statusUrl"`
	ExpiresAt    time.Time         `json:"expiresAt"`
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
	if err := decodeBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	session, err := s.uploads.Create(r.Context(), upload.CreateInput{
		LogicPath: request.Path, Size: request.Size, ContentType: request.ContentType, Overwrite: request.Overwrite,
		TargetVersion: request.TargetVersion,
		Origin:        requestOrigin(r),
	})
	if err != nil {
		writeUploadError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, toUploadResponse(session, true))
}

func (s *Server) handlePreflightUploads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "uploads are not configured")
		return
	}
	var request preflightUploadRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(request.Items) == 0 || len(request.Items) > 1000 {
		writeError(w, http.StatusBadRequest, "items must contain between 1 and 1000 entries")
		return
	}
	inputs := make([]upload.PreflightInput, len(request.Items))
	for index, item := range request.Items {
		inputs[index] = upload.PreflightInput{ClientID: item.ClientID, LogicPath: item.Path}
	}
	results, err := s.uploads.Preflight(r.Context(), inputs)
	if err != nil {
		writeUploadError(w, err)
		return
	}
	writeJSON(w, preflightUploadResponse{Items: results})
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
			writeJSON(w, toUploadResponse(session, true))
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
		start, end, total, err := parseUploadContentRange(r.Header.Get("Content-Range"), r.ContentLength)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		session, err := s.uploads.WriteChunk(r.Context(), id, start, end, total, r.Body)
		setCommittedRange(w, session.UploadedSize)
		if err != nil {
			writeUploadError(w, err)
			return
		}
		if session.UploadedSize < session.Size {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(308)
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
	if session.Status == upload.StatusComplete || session.Status == upload.StatusExpired {
		includeUploadCapability = false
	}
	uploadURL := ""
	if includeUploadCapability {
		uploadURL = session.UploadURL
	}
	if includeUploadCapability && uploadURL == "" && session.Driver != "gcs" {
		uploadURL = "/api/uploads/" + session.ID + "/content"
	}
	var headers map[string]string
	if includeUploadCapability {
		headers = session.UploadHeaders
		if headers == nil {
			headers = map[string]string{}
		}
		if strings.TrimSpace(session.ContentType) != "" {
			headers["Content-Type"] = session.ContentType
		}
	}
	if headers == nil {
		headers = map[string]string{}
	}
	return uploadResponse{
		ID: session.ID, LogicPath: session.LogicPath, Size: session.Size, UploadedSize: session.UploadedSize, ContentType: session.ContentType,
		Status: session.Status, Error: session.Error, Method: http.MethodPut, UploadURL: uploadURL, Headers: headers,
		CompleteURL: "/api/uploads/" + session.ID + "/complete", StatusURL: "/api/uploads/" + session.ID,
		ExpiresAt: session.ExpiresAt,
	}
}

func parseUploadContentRange(value string, contentLength int64) (start, end, total int64, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if contentLength < 0 {
			return 0, 0, 0, errors.New("Content-Range is required when Content-Length is unknown")
		}
		if contentLength == 0 {
			return 0, -1, 0, nil
		}
		return 0, contentLength - 1, contentLength, nil
	}
	if strings.HasPrefix(value, "bytes */") {
		total, err = parseNonNegativeInt(strings.TrimPrefix(value, "bytes */"))
		if err != nil || total != 0 || contentLength > 0 {
			return 0, 0, 0, errors.New("invalid Content-Range")
		}
		return 0, -1, 0, nil
	}
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	rangeAndTotal := strings.TrimPrefix(value, "bytes ")
	rangePart, totalPart, ok := strings.Cut(rangeAndTotal, "/")
	if !ok {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	startPart, endPart, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	start, err = parseNonNegativeInt(startPart)
	if err != nil {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	end, err = parseNonNegativeInt(endPart)
	if err != nil {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	total, err = parseNonNegativeInt(totalPart)
	if err != nil || total == 0 || end < start || end >= total {
		return 0, 0, 0, errors.New("invalid Content-Range")
	}
	if contentLength >= 0 && contentLength != end-start+1 {
		return 0, 0, 0, errors.New("Content-Length does not match Content-Range")
	}
	return start, end, total, nil
}

func parseNonNegativeInt(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("empty integer")
	}
	var parsed int64
	for _, character := range value {
		if character < '0' || character > '9' || parsed > (1<<63-1-int64(character-'0'))/10 {
			return 0, errors.New("invalid integer")
		}
		parsed = parsed*10 + int64(character-'0')
	}
	return parsed, nil
}

func setCommittedRange(w http.ResponseWriter, uploadedSize int64) {
	if uploadedSize > 0 {
		w.Header().Set("Range", "bytes=0-"+strconv.FormatInt(uploadedSize-1, 10))
	}
}

func writeUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upload.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, db.ErrMetadataRateLimit):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, db.ErrMetadataConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, upload.ErrFileExists):
		writeCodedError(w, http.StatusConflict, "UPLOAD_TARGET_EXISTS", err.Error())
	case errors.Is(err, upload.ErrTargetIsDirectory):
		writeCodedError(w, http.StatusConflict, "UPLOAD_TARGET_IS_DIRECTORY", err.Error())
	case errors.Is(err, upload.ErrConflict), errors.Is(err, upload.ErrInvalidSession):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, upload.ErrTargetChanged):
		writeCodedError(w, http.StatusConflict, "UPLOAD_TARGET_CHANGED", err.Error())
	case errors.Is(err, upload.ErrOffsetConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, upload.ErrExpired):
		writeError(w, http.StatusGone, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
