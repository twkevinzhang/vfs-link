package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/db"
)

type Server struct {
	store   *db.Store
	objects blob.Store
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
	Path        string  `json:"path"`
	Breadcrumbs []Entry `json:"breadcrumbs"`
	Entries     []Entry `json:"entries"`
	Stats       Stats   `json:"stats"`
	GeneratedAt string  `json:"generatedAt"`
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
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Kind     string      `json:"kind"`
	Children []*TreeNode `json:"children,omitempty"`
}

func New(store *db.Store, objects blob.Store) *Server {
	return &Server{store: store, objects: objects}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/files", s.handleFiles)
	mux.HandleFunc("/api/tree", s.handleTree)
	mux.HandleFunc("/api/download", s.handleDownload)
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

	entries, err := s.directChildren(r.Context(), logicPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stats, err := s.stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, FilesResponse{
		Path:        logicPath,
		Breadcrumbs: breadcrumbs(logicPath),
		Entries:     entries,
		Stats:       stats,
		GeneratedAt: time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	records, err := s.store.ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root := TreeNode{Name: "/", Path: "/", Kind: "directory"}
	nodes := map[string]*TreeNode{"/": &root}
	sort.Slice(records, func(i, j int) bool { return records[i].LogicPath < records[j].LogicPath })
	for _, record := range records {
		node := &TreeNode{
			Name: path.Base(record.LogicPath),
			Path: record.LogicPath,
			Kind: kind(record),
		}
		nodes[record.LogicPath] = node
		parentPath := path.Dir(record.LogicPath)
		if parentPath == "." {
			parentPath = "/"
		}
		parent, ok := nodes[parentPath]
		if !ok {
			continue
		}
		parent.Children = append(parent.Children, node)
	}
	writeJSON(w, root)
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

func (s *Server) directChildren(ctx context.Context, dirPath string) ([]Entry, error) {
	prefix := withTrailingSlash(dirPath)
	records, err := s.store.ListPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(records))
	for _, record := range records {
		suffix := strings.TrimPrefix(record.LogicPath, prefix)
		if suffix == "" || strings.Contains(suffix, "/") {
			continue
		}
		entries = append(entries, entryFromRecord(record))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == "directory"
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
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

func withTrailingSlash(value string) string {
	if value == "/" {
		return "/"
	}
	if strings.HasSuffix(value, "/") {
		return value
	}
	return value + "/"
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
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
