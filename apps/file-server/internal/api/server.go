package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	driftdomain "github.com/twkevinzhang/vfs-link/apps/file-server/internal/drift"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/share"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/upload"
)

const (
	defaultFilePageLimit = 100
	maxFilePageLimit     = 500
)

type Server struct {
	store            db.Store
	objects          blob.Store
	thumbnailObjects blob.Store
	shares           *share.Service
	uploads          *upload.Service
	files            *fileops.Service
	drift            *driftdomain.Service
	driftErr         error
	driftEnabled     bool
	webHandler       http.Handler
	cors             map[string]struct{}
}

// SetDriftEnabled is an explicit safety gate. Drift routes are wired by
// default for observability, but destructive plans/actions remain disabled
// until configuration opts in.
func (s *Server) SetDriftEnabled(enabled bool) *Server {
	s.driftEnabled = enabled
	return s
}

func (s *Server) SetCORSOrigins(origins []string) *Server {
	s.cors = make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if origin = strings.TrimSpace(origin); origin != "" {
			s.cors[origin] = struct{}{}
		}
	}
	return s
}

type Entry struct {
	Name          string             `json:"name"`
	Path          string             `json:"path"`
	Kind          string             `json:"kind"`
	Size          int64              `json:"size"`
	FolderSummary *FolderSummary     `json:"folderSummary,omitempty"`
	UpdatedAt     time.Time          `json:"updatedAt"`
	PhysicalHash  string             `json:"physicalHash,omitempty"`
	TrashID       string             `json:"trashId,omitempty"`
	TrashedAt     *time.Time         `json:"trashedAt,omitempty"`
	Thumbnail     *thumbnailResponse `json:"thumbnail,omitempty"`
}

type FilesResponse struct {
	Path          string        `json:"path"`
	Breadcrumbs   []Entry       `json:"breadcrumbs"`
	Entries       []Entry       `json:"entries"`
	Pagination    Pagination    `json:"pagination"`
	FolderSummary FolderSummary `json:"folderSummary"`
	VisibleBytes  int64         `json:"visibleBytes"`
	Stats         *Stats        `json:"stats,omitempty"`
	GeneratedAt   string        `json:"generatedAt"`
}

// FolderSummary describes the complete active subtree rooted at a directory.
// It is intentionally distinct from visibleBytes, which only describes direct
// children matching the current list query.
type FolderSummary struct {
	Files       int64 `json:"files"`
	Directories int64 `json:"directories"`
	Bytes       int64 `json:"bytes"`
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
	FileCount      int   `json:"fileCount"`
	DirectoryCount int   `json:"directoryCount"`
	TotalBytes     int64 `json:"totalBytes"`
	ObjectCount    int   `json:"objectCount"`
	ObjectBytes    int64 `json:"objectBytes"`
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

// New keeps original archive objects and derived thumbnail objects in
// deliberately separate stores.  Callers must provide both explicitly: using
// the archive store as an implicit thumbnail fallback makes a bucket routing
// mistake silently write derived data into the primary archive bucket.
func New(store db.Store, objects blob.Store, thumbnailObjects blob.Store, shares *share.Service, webStaticRoot string, webBasePath string, uploads ...*upload.Service) *Server {
	if thumbnailObjects == nil {
		panic("thumbnail object store is required")
	}
	driftService, driftErr := driftdomain.New(store, objects)
	server := &Server{
		store:            store,
		objects:          objects,
		thumbnailObjects: thumbnailObjects,
		shares:           shares,
		files:            fileops.New(store, objects, thumbnailObjects),
		drift:            driftService,
		driftErr:         driftErr,
		webHandler:       newWebHandler(webStaticRoot, webBasePath),
	}
	if len(uploads) > 0 {
		server.uploads = uploads[0]
	}
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", handleOpenAPI)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/files", s.handleFiles)
	mux.HandleFunc("/api/files/move", s.handleMoveFiles)
	mux.HandleFunc("/api/files/rename", s.handleRenameFile)
	mux.HandleFunc("/api/operations/", s.handleOperation)
	mux.HandleFunc("/api/files/trash", s.handleTrashFiles)
	mux.HandleFunc("/api/trash", s.handleTrash)
	mux.HandleFunc("/api/trash/restore", s.handleRestoreTrash)
	mux.HandleFunc("/api/trash/delete", s.handleDeleteTrash)
	mux.HandleFunc("/api/trash/empty", s.handleEmptyTrash)
	mux.HandleFunc("/api/tree", s.handleTree)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/shares/drafts", s.handleCreateShareDraft)
	mux.HandleFunc("/api/shares/", s.handleShare)
	mux.HandleFunc("/api/uploads", s.handleCreateUpload)
	mux.HandleFunc("/api/uploads/", s.handleUpload)
	mux.HandleFunc("/api/thumbnails", s.handleThumbnails)
	mux.HandleFunc("/api/thumbnails/", s.handleThumbnail)
	mux.HandleFunc("/api/drift", s.handleDrift)
	mux.HandleFunc("/api/drift/plans", s.handleDriftPlans)
	mux.HandleFunc("/api/drift/actions", s.handleDriftActions)
	mux.HandleFunc("/api/drift/actions/", s.handleDriftAction)
	if s.webHandler != nil {
		mux.Handle("/", s.webHandler)
	}
	return withCORS(mux, s.cors)
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
	logicPath, err := logicpath.Parse(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if logicPath != "" {
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
	entries, err := s.entriesWithThumbnails(r.Context(), page.Records)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

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
		FolderSummary: folderSummaryFromDB(page.FolderSummary),
		VisibleBytes:  page.TotalBytes,
		GeneratedAt:   time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	logicPath, err := logicpath.Parse(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if logicPath != "" {
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
	if logicPath == "" {
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
	logicPath, err := logicpath.Parse(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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

	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" {
		disposition = "inline"
	}
	contentType := mime.TypeByExtension(path.Ext(record.LogicPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{
		"filename": path.Base(record.LogicPath),
	}))
	w.Header().Set("Content-Type", contentType)
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

func (s *Server) entriesWithThumbnails(ctx context.Context, records []db.FileRecord) ([]Entry, error) {
	entries := entriesFromRecords(records)
	fileIDs := make([]int, 0, len(records))
	for _, record := range records {
		if !record.IsDirectory {
			fileIDs = append(fileIDs, record.ID)
		}
	}
	thumbnails, err := s.store.FindThumbnailsForFiles(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	for index, record := range records {
		if thumbnail, ok := thumbnails[record.ID]; ok {
			response := thumbnailToResponse(thumbnail)
			entries[index].Thumbnail = &response
		}
	}
	return entries, nil
}

func (s *Server) stats(ctx context.Context) (Stats, error) {
	if provider, ok := s.store.(db.MetadataStatsProvider); ok {
		metadataStats, err := provider.MetadataStats(ctx)
		if err != nil {
			return Stats{}, err
		}
		return Stats{
			FileCount:      int(metadataStats.LogicalFiles),
			DirectoryCount: int(metadataStats.LogicalDirs),
			TotalBytes:     metadataStats.LogicalBytes,
			ObjectCount:    int(metadataStats.PhysicalObjects),
			ObjectBytes:    metadataStats.PhysicalBytes,
		}, nil
	}

	// PostgreSQL does not maintain a separate aggregate yet. Derive both the
	// logical and referenced-object totals from metadata so status never has to
	// enumerate an entire physical bucket.
	records, err := s.store.ListAll(ctx)
	if err != nil {
		return Stats{}, err
	}
	var stats Stats
	physicalObjects := make(map[string]int64)
	for _, record := range records {
		if record.IsDirectory {
			stats.DirectoryCount++
			continue
		}
		stats.FileCount++
		stats.TotalBytes += record.Size
		if record.PhysicalHash != "" {
			physicalObjects[record.PhysicalHash] = record.Size
		}
	}
	stats.ObjectCount = len(physicalObjects)
	for _, size := range physicalObjects {
		stats.ObjectBytes += size
	}
	return stats, nil
}

func entryFromRecord(record db.FileRecord) Entry {
	entry := Entry{
		Name:         path.Base(record.LogicPath),
		Path:         record.LogicPath,
		Kind:         kind(record),
		Size:         record.Size,
		UpdatedAt:    record.UpdatedAt,
		PhysicalHash: record.PhysicalHash,
		TrashID:      record.TrashID,
		TrashedAt:    record.TrashedAt,
	}
	if record.FolderSummary != nil {
		summary := folderSummaryFromDB(*record.FolderSummary)
		entry.FolderSummary = &summary
	}
	return entry
}

func folderSummaryFromDB(summary db.FolderSummary) FolderSummary {
	return FolderSummary{
		Files:       summary.Files,
		Directories: summary.Directories,
		Bytes:       summary.Bytes,
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
	if logicPath == "" {
		return []Entry{{Name: "/", Path: "", Kind: "directory"}}
	}
	crumbs := []Entry{{Name: "/", Path: "", Kind: "directory"}}
	current := ""
	for _, part := range strings.Split(logicPath, "/") {
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		crumbs = append(crumbs, Entry{Name: part, Path: current, Kind: "directory"})
	}
	return crumbs
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

func withCORS(next http.Handler, allowed map[string]struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		_, wildcard := allowed["*"]
		_, explicit := allowed[origin]
		if origin != "" && (wildcard || explicit) {
			if wildcard {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions && origin != "" && (wildcard || explicit) {
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
