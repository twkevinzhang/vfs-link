package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
)

const maxThumbnailBytes = 2 << 20

type thumbnailResponse struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func thumbnailToResponse(record db.ThumbnailRecord) thumbnailResponse {
	return thumbnailResponse{ID: record.ID, URL: "/api/thumbnails/" + record.ID, Width: record.Width, Height: record.Height}
}

func (s *Server) handleThumbnails(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/thumbnails" {
		writeError(w, http.StatusNotFound, "thumbnail not found")
		return
	}
	if r.Method == http.MethodDelete {
		s.handleDeleteThumbnails(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxThumbnailBytes+128*1024)
	if err := r.ParseMultipartForm(maxThumbnailBytes + 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "invalid thumbnail form")
		return
	}
	var rawPaths []string
	if err := json.Unmarshal([]byte(r.FormValue("paths")), &rawPaths); err != nil || len(rawPaths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one archive path is required")
		return
	}
	width, widthErr := strconv.Atoi(r.FormValue("width"))
	height, heightErr := strconv.Atoi(r.FormValue("height"))
	if widthErr != nil || heightErr != nil || width < 1 || height < 1 || width > 512 || height > 512 {
		writeError(w, http.StatusBadRequest, "thumbnail dimensions must be between 1 and 512 pixels")
		return
	}
	file, _, err := r.FormFile("thumbnail")
	if err != nil {
		writeError(w, http.StatusBadRequest, "thumbnail file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxThumbnailBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxThumbnailBytes {
		writeError(w, http.StatusBadRequest, "thumbnail must be a non-empty WebP up to 2 MiB")
		return
	}
	if http.DetectContentType(data) != "image/webp" {
		writeError(w, http.StatusUnsupportedMediaType, "thumbnail must be WebP")
		return
	}

	fileIDs := make([]int, 0, len(rawPaths))
	seen := make(map[int]bool)
	for _, rawPath := range rawPaths {
		parsed, parseErr := logicpath.Parse(rawPath)
		if parseErr != nil || parsed == "" {
			writeError(w, http.StatusBadRequest, "invalid archive path")
			return
		}
		record, found, findErr := s.store.Find(r.Context(), parsed)
		if findErr != nil {
			writeError(w, http.StatusInternalServerError, findErr.Error())
			return
		}
		if !found || record.IsDirectory {
			writeError(w, http.StatusNotFound, "archive file not found")
			return
		}
		if !seen[record.ID] {
			seen[record.ID] = true
			fileIDs = append(fileIDs, record.ID)
		}
	}

	id := uuid.NewString()
	objectName := "_vfs-link-thumbnails/" + id + ".webp"
	writer, err := s.thumbnailObjects.NewWriter(r.Context(), objectName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err = io.Copy(writer, bytes.NewReader(data)); err != nil {
		_ = writer.Close()
		_ = s.thumbnailObjects.Delete(r.Context(), objectName)
		writeError(w, http.StatusInternalServerError, "write thumbnail: "+err.Error())
		return
	}
	if err = writer.Close(); err != nil {
		_ = s.thumbnailObjects.Delete(r.Context(), objectName)
		writeError(w, http.StatusInternalServerError, "commit thumbnail: "+err.Error())
		return
	}
	record := db.ThumbnailRecord{ID: id, PhysicalHash: objectName, ContentType: "image/webp", Size: int64(len(data)), Width: width, Height: height, CreatedAt: time.Now().UTC()}
	orphans, err := s.store.ReplaceThumbnail(r.Context(), record, fileIDs)
	if err != nil {
		// A TreeStore replacement spans multiple conditionally-written metadata
		// objects. If it fails after publishing the new thumbnail record, deleting
		// the WebP here would leave an already-published file link broken. Delete
		// only when the metadata store can positively confirm that publication did
		// not happen; otherwise retain a recoverable orphan for the GC path.
		if _, found, findErr := s.store.FindThumbnail(r.Context(), id); findErr == nil && !found {
			_ = s.thumbnailObjects.Delete(r.Context(), objectName)
		}
		writeError(w, http.StatusInternalServerError, "store thumbnail: "+err.Error())
		return
	}
	for _, orphan := range orphans {
		_ = s.thumbnailObjects.Delete(r.Context(), orphan.PhysicalHash)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, thumbnailToResponse(record))
}

func (s *Server) handleDeleteThumbnails(w http.ResponseWriter, r *http.Request) {
	var request pathsRequest
	if decodeBody(r, &request) != nil || len(request.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one archive path is required")
		return
	}
	fileIDs := make([]int, 0, len(request.Paths))
	for _, rawPath := range request.Paths {
		parsed, err := logicpath.Parse(rawPath)
		if err != nil || parsed == "" {
			writeError(w, http.StatusBadRequest, "invalid archive path")
			return
		}
		record, found, err := s.store.Find(r.Context(), parsed)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if found && !record.IsDirectory {
			fileIDs = append(fileIDs, record.ID)
		}
	}
	orphans, err := s.store.DetachThumbnails(r.Context(), fileIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, orphan := range orphans {
		_ = s.thumbnailObjects.Delete(r.Context(), orphan.PhysicalHash)
	}
	writeJSON(w, deletedResponse{Deleted: int64(len(orphans))})
}

func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/thumbnails/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "thumbnail not found")
		return
	}
	record, found, err := s.store.FindThumbnail(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "thumbnail not found")
		return
	}
	reader, err := s.thumbnailObjects.NewReader(r.Context(), record.PhysicalHash)
	if err != nil {
		writeError(w, http.StatusNotFound, "thumbnail object not found")
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Content-Length", fmt.Sprint(record.Size))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = io.Copy(w, reader)
}
