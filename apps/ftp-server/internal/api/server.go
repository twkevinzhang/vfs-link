package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/share"
)

const (
	defaultFilePageLimit = 100
	maxFilePageLimit     = 500
)

type Server struct {
	store      *db.Store
	objects    blob.Store
	shares     *share.Service
	webHandler http.Handler
}

type Entry struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Kind         string    `json:"kind"`
	Size         int64     `json:"size"`
	UpdatedAt    time.Time `json:"updatedAt"`
	PhysicalHash string    `json:"physicalHash,omitempty"`
}

type FilesResponse struct {
	Path         string     `json:"path"`
	Breadcrumbs  []Entry    `json:"breadcrumbs"`
	Entries      []Entry    `json:"entries"`
	Pagination   Pagination `json:"pagination"`
	VisibleBytes int64      `json:"visibleBytes"`
	Stats        *Stats     `json:"stats,omitempty"`
	GeneratedAt  string     `json:"generatedAt"`
}

type Pagination struct {
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
	Total   int    `json:"total"`
	Query   string `json:"query"`
	HasNext bool   `json:"hasNext"`
	HasPrev bool   `json:"hasPrev"`
}

type Stats struct {
	FileCount        int   `json:"fileCount"`
	DirectoryCount   int   `json:"directoryCount"`
	TotalBytes       int64 `json:"totalBytes"`
	LocalObjectCount int   `json:"localObjectCount"`
	LocalObjectBytes int64 `json:"localObjectBytes"`
}

type StatusResponse struct {
	StorageDriver string `json:"storageDriver"`
	StorageRoot   string `json:"storageRoot"`
	Stats         Stats  `json:"stats"`
	GeneratedAt   string `json:"generatedAt"`
}

type TreeNode struct {
	Name        string      `json:"name"`
	Path        string      `json:"path"`
	Kind        string      `json:"kind"`
	Size        int64       `json:"size"`
	UpdatedAt   time.Time   `json:"updatedAt"`
	HasChildren bool        `json:"hasChildren,omitempty"`
	Children    []*TreeNode `json:"children,omitempty"`
}

type createShareDraftRequest struct {
	Path string `json:"path"`
}

type shareResponse struct {
	ID                 string     `json:"id"`
	LogicPath          string     `json:"logicPath"`
	FileName           string     `json:"fileName"`
	Size               int64      `json:"size"`
	DestinationObject  string     `json:"destinationObject"`
	DestinationURL     string     `json:"destinationUrl"`
	ShareURL           string     `json:"shareUrl"`
	Email              string     `json:"email"`
	NotificationTarget string     `json:"notificationTarget"`
	Status             string     `json:"status"`
	Error              string     `json:"error,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
	NotifiedAt         *time.Time `json:"notifiedAt,omitempty"`
}

func New(store *db.Store, objects blob.Store, shares *share.Service, webStaticRoot string, webBasePath string) *Server {
	return &Server{
		store:      store,
		objects:    objects,
		shares:     shares,
		webHandler: newWebHandler(webStaticRoot, webBasePath),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/files", s.handleFiles)
	mux.HandleFunc("/api/tree", s.handleTree)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/shares/drafts", s.handleCreateShareDraft)
	mux.HandleFunc("/api/shares/", s.handleShare)
	if s.webHandler != nil {
		mux.Handle("/", s.webHandler)
	}
	return withCORS(mux)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stats, err := s.stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, StatusResponse{
		StorageDriver: s.objects.Driver(),
		StorageRoot:   s.objects.Root(),
		Stats:         stats,
		GeneratedAt:   time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	logicPath := cleanPath(r.URL.Query().Get("path"))
	if logicPath != "/" {
		record, found, err := s.store.Find(r.Context(), logicPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found || !record.IsDirectory {
			writeError(w, http.StatusNotFound, "directory not found")
			return
		}
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, offset := parsePagination(r)
	page, err := s.store.ListDirectChildren(r.Context(), logicPath, db.DirectChildrenOptions{
		Query:  query,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries := entriesFromRecords(page.Records)

	writeJSON(w, FilesResponse{
		Path:        logicPath,
		Breadcrumbs: breadcrumbs(logicPath),
		Entries:     entries,
		Pagination: Pagination{
			Limit:   limit,
			Offset:  offset,
			Total:   page.Total,
			Query:   query,
			HasNext: offset+len(entries) < page.Total,
			HasPrev: offset > 0,
		},
		VisibleBytes: page.TotalBytes,
		GeneratedAt:  time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	logicPath := cleanPath(r.URL.Query().Get("path"))
	if logicPath != "/" {
		record, found, err := s.store.Find(r.Context(), logicPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found || !record.IsDirectory {
			writeError(w, http.StatusNotFound, "directory not found")
			return
		}
	}

	page, err := s.store.ListDirectChildren(r.Context(), logicPath, db.DirectChildrenOptions{
		DirectoriesOnly: true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	node := TreeNode{Name: path.Base(logicPath), Path: logicPath, Kind: "directory"}
	if logicPath == "/" {
		node.Name = "/"
	}
	for _, record := range page.Records {
		node.Children = append(node.Children, &TreeNode{
			Name:      path.Base(record.LogicPath),
			Path:      record.LogicPath,
			Kind:      kind(record),
			Size:      record.Size,
			UpdatedAt: record.UpdatedAt,
		})
	}
	writeJSON(w, node)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	logicPath := cleanPath(r.URL.Query().Get("path"))
	record, found, err := s.store.Find(r.Context(), logicPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found || record.IsDirectory {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	reader, err := s.objects.NewReader(r.Context(), record.PhysicalHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(record.LogicPath)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", record.Size))
	if _, err := io.Copy(w, reader); err != nil {
		return
	}
}

func (s *Server) handleCreateShareDraft(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request createShareDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	record, err := s.shares.CreateDraft(r.Context(), request.Path)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, toShareResponse(record))
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/shares/")
	parts := strings.Split(strings.Trim(suffix, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}

	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		record, found, err := s.shares.Find(r.Context(), id)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "share not found")
			return
		}
		writeJSON(w, toShareResponse(record))
		return
	}

	if len(parts) == 2 && parts[1] == "start" && r.Method == http.MethodPost {
		record, err := s.shares.Start(r.Context(), id)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, toShareResponse(record))
		return
	}

	writeError(w, http.StatusNotFound, "share endpoint not found")
}

func entriesFromRecords(records []db.FileRecord) []Entry {
	entries := make([]Entry, 0, len(records))
	for _, record := range records {
		entries = append(entries, entryFromRecord(record))
	}
	return entries
}

func (s *Server) stats(ctx context.Context) (Stats, error) {
	records, err := s.store.ListAll(ctx)
	if err != nil {
		return Stats{}, err
	}
	var stats Stats
	for _, record := range records {
		if record.IsDirectory {
			stats.DirectoryCount++
			continue
		}
		stats.FileCount++
		stats.TotalBytes += record.Size
	}
	objects, err := s.objects.List(ctx)
	if err != nil {
		return Stats{}, err
	}
	stats.LocalObjectCount = len(objects)
	for _, object := range objects {
		stats.LocalObjectBytes += object.Size
	}
	return stats, nil
}

func entryFromRecord(record db.FileRecord) Entry {
	return Entry{
		Name:         path.Base(record.LogicPath),
		Path:         record.LogicPath,
		Kind:         kind(record),
		Size:         record.Size,
		UpdatedAt:    record.UpdatedAt,
		PhysicalHash: record.PhysicalHash,
	}
}

func kind(record db.FileRecord) string {
	if record.IsDirectory {
		return "directory"
	}
	return "file"
}

func toShareResponse(record db.ShareRecord) shareResponse {
	return shareResponse{
		ID:                 record.ID,
		LogicPath:          record.LogicPath,
		FileName:           record.FileName,
		Size:               record.Size,
		DestinationObject:  record.DestinationObject,
		DestinationURL:     record.ShareURL,
		ShareURL:           record.ShareURL,
		Email:              record.Email,
		NotificationTarget: record.Email,
		Status:             record.Status,
		Error:              record.Error,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
		CompletedAt:        record.CompletedAt,
		NotifiedAt:         record.NotifiedAt,
	}
}

func breadcrumbs(logicPath string) []Entry {
	if logicPath == "/" {
		return []Entry{{Name: "/", Path: "/", Kind: "directory"}}
	}
	crumbs := []Entry{{Name: "/", Path: "/", Kind: "directory"}}
	current := ""
	for _, part := range strings.Split(strings.Trim(logicPath, "/"), "/") {
		current += "/" + part
		crumbs = append(crumbs, Entry{Name: part, Path: current, Kind: "directory"})
	}
	return crumbs
}

func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value)
}

func parsePagination(r *http.Request) (int, int) {
	limit := parseBoundedInt(r.URL.Query().Get("limit"), defaultFilePageLimit, 1, maxFilePageLimit)
	offset := parseBoundedInt(r.URL.Query().Get("offset"), 0, 0, 1<<31-1)
	return limit, offset
}

func parseBoundedInt(value string, fallback int, minimum int, maximum int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	if parsed < minimum {
		return minimum
	}
	if parsed > maximum {
		return maximum
	}
	return parsed
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func writeAPIError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}
