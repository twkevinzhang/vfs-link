package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

const (
	// ReservedMetadataObject is deliberately outside the user object namespace.
	ReservedMetadataObject = "_vfs-link/metadata.json"
	jsonSchemaVersion      = 1
	jsonCASAttempts        = 8
)

var ErrJSONConflict = errors.New("JSON metadata changed concurrently")

type jsonState struct {
	Version    int             `json:"version"`
	NextFileID int             `json:"nextFileId"`
	Files      []FileRecord    `json:"files"`
	Shares     []ShareRecord   `json:"shares"`
	DAVLocks   []DAVLockRecord `json:"davLocks"`
	Uploads    []UploadRecord  `json:"uploads"`
}

type jsonBackend interface {
	Load(context.Context) (data []byte, generation int64, exists bool, err error)
	Save(context.Context, []byte, int64, bool) error
	Close() error
}

type JSONStore struct {
	mu      *sync.Mutex
	backend jsonBackend
}

var localJSONLocks sync.Map

var _ Store = (*JSONStore)(nil)

func NewJSONLocal(filename string) (Store, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, fmt.Errorf("JSON metadata filename is required")
	}
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve JSON metadata filename: %w", err)
	}
	lock, _ := localJSONLocks.LoadOrStore(abs, &sync.Mutex{})
	return &JSONStore{mu: lock.(*sync.Mutex), backend: &localJSONBackend{filename: abs}}, nil
}

func NewJSONGCS(ctx context.Context, bucket, object string) (Store, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS metadata client: %w", err)
	}
	store, err := NewJSONGCSWithClient(client, bucket, object)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	store.(*JSONStore).backend.(*gcsJSONBackend).ownsClient = true
	return store, nil
}

func NewJSONGCSWithClient(client *storage.Client, bucket, object string) (Store, error) {
	if client == nil {
		return nil, fmt.Errorf("GCS client is required")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("GCS metadata bucket is required")
	}
	object = strings.TrimPrefix(strings.TrimSpace(object), "/")
	if object == "" {
		object = ReservedMetadataObject
	}
	return &JSONStore{mu: &sync.Mutex{}, backend: &gcsJSONBackend{client: client, bucket: bucket, object: object}}, nil
}

func (s *JSONStore) Close() { _ = s.backend.Close() }

func (s *JSONStore) EnsureSchema(ctx context.Context) error {
	_, err := s.mutate(ctx, func(*jsonState) (any, bool, error) { return nil, false, nil })
	return err
}

func emptyJSONState() jsonState { return jsonState{Version: jsonSchemaVersion, NextFileID: 1} }

func decodeJSONState(data []byte, exists bool) (jsonState, error) {
	if !exists || len(data) == 0 {
		return emptyJSONState(), nil
	}
	var state jsonState
	if err := json.Unmarshal(data, &state); err != nil {
		return jsonState{}, fmt.Errorf("decode JSON metadata: %w", err)
	}
	if state.Version != jsonSchemaVersion {
		return jsonState{}, fmt.Errorf("unsupported JSON metadata version %d", state.Version)
	}
	if state.NextFileID < 1 {
		state.NextFileID = 1
		for _, f := range state.Files {
			if f.ID >= state.NextFileID {
				state.NextFileID = f.ID + 1
			}
		}
	}
	return state, nil
}

func (s *JSONStore) read(ctx context.Context) (jsonState, error) {
	if s.mu == nil {
		s.mu = &sync.Mutex{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _, exists, err := s.backend.Load(ctx)
	if err != nil {
		return jsonState{}, err
	}
	return decodeJSONState(data, exists)
}

func (s *JSONStore) mutate(ctx context.Context, fn func(*jsonState) (any, bool, error)) (any, error) {
	if s.mu == nil {
		s.mu = &sync.Mutex{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for attempt := 0; attempt < jsonCASAttempts; attempt++ {
		data, generation, exists, err := s.backend.Load(ctx)
		if err != nil {
			return nil, err
		}
		state, err := decodeJSONState(data, exists)
		if err != nil {
			return nil, err
		}
		result, changed, err := fn(&state)
		if err != nil {
			return nil, err
		}
		if !changed && exists {
			return result, nil
		}
		encoded, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode JSON metadata: %w", err)
		}
		encoded = append(encoded, '\n')
		err = s.backend.Save(ctx, encoded, generation, exists)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, ErrJSONConflict) {
			return nil, err
		}
		delay := time.Duration(5+rand.Intn(20*(attempt+1))) * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, fmt.Errorf("%w after %d attempts", ErrJSONConflict, jsonCASAttempts)
}

type localJSONBackend struct{ filename string }

func (b *localJSONBackend) Load(_ context.Context) ([]byte, int64, bool, error) {
	data, err := os.ReadFile(b.filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	return data, 0, err == nil, err
}
func (b *localJSONBackend) Save(_ context.Context, data []byte, _ int64, _ bool) error {
	dir := filepath.Dir(b.filename)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".metadata-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, b.filename); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (*localJSONBackend) Close() error { return nil }

type gcsJSONBackend struct {
	client         *storage.Client
	bucket, object string
	ownsClient     bool
}

func (b *gcsJSONBackend) Load(ctx context.Context) ([]byte, int64, bool, error) {
	r, err := b.client.Bucket(b.bucket).Object(b.object).NewReader(ctx)
	if isGCSNotFound(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("read GCS JSON metadata: %w", err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read GCS JSON metadata body: %w", err)
	}
	return data, r.Attrs.Generation, true, nil
}
func (b *gcsJSONBackend) Save(ctx context.Context, data []byte, generation int64, exists bool) error {
	conditions := storage.Conditions{DoesNotExist: !exists}
	if exists {
		conditions = storage.Conditions{GenerationMatch: generation}
	}
	w := b.client.Bucket(b.bucket).Object(b.object).If(conditions).NewWriter(ctx)
	w.ContentType = "application/json"
	w.CacheControl = "no-store"
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return classifyGCSSaveError(err)
	}
	return classifyGCSSaveError(w.Close())
}
func (b *gcsJSONBackend) Close() error {
	if b.ownsClient {
		return b.client.Close()
	}
	return nil
}
func isGCSNotFound(err error) bool {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return true
	}
	var e *googleapi.Error
	return errors.As(err, &e) && e.Code == 404
}
func classifyGCSSaveError(err error) error {
	if err == nil {
		return nil
	}
	var e *googleapi.Error
	if errors.As(err, &e) && (e.Code == 409 || e.Code == 412) {
		return ErrJSONConflict
	}
	return fmt.Errorf("write GCS JSON metadata: %w", err)
}

func (s *JSONStore) Find(ctx context.Context, logicPath string) (FileRecord, bool, error) {
	st, err := s.read(ctx)
	if err != nil {
		return FileRecord{}, false, err
	}
	for _, r := range st.Files {
		if r.TrashedAt == nil && r.LogicPath == logicPath {
			return r, true, nil
		}
	}
	return FileRecord{}, false, nil
}
func (s *JSONStore) ListAll(ctx context.Context) ([]FileRecord, error) {
	st, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]FileRecord, 0, len(st.Files))
	for _, record := range st.Files {
		if record.TrashedAt == nil {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LogicPath < result[j].LogicPath })
	return result, nil
}
func (s *JSONStore) ListPrefix(ctx context.Context, prefix string) ([]FileRecord, error) {
	all, err := s.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]FileRecord, 0)
	for _, r := range all {
		if strings.HasPrefix(r.LogicPath, prefix) {
			result = append(result, r)
		}
	}
	return result, nil
}
func (s *JSONStore) ListDirectChildren(ctx context.Context, dirPath string, o DirectChildrenOptions) (DirectChildrenPage, error) {
	all, err := s.ListAll(ctx)
	if err != nil {
		return DirectChildrenPage{}, err
	}
	prefix, q := withTrailingSlash(dirPath), strings.ToLower(strings.TrimSpace(o.Query))
	var page DirectChildrenPage
	for _, r := range all {
		suffix := strings.TrimPrefix(r.LogicPath, prefix)
		if r.LogicPath == dirPath || suffix == r.LogicPath || suffix == "" || strings.Contains(suffix, "/") {
			continue
		}
		if o.DirectoriesOnly && !r.IsDirectory {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(suffix), q) && !strings.Contains(strings.ToLower(r.LogicPath), q) {
			continue
		}
		page.Records = append(page.Records, r)
		page.Total++
		if !r.IsDirectory {
			page.TotalBytes += r.Size
		}
	}
	sort.Slice(page.Records, func(i, j int) bool {
		if page.Records[i].IsDirectory != page.Records[j].IsDirectory {
			return page.Records[i].IsDirectory
		}
		return strings.TrimPrefix(page.Records[i].LogicPath, prefix) < strings.TrimPrefix(page.Records[j].LogicPath, prefix)
	})
	start := o.Offset
	if start < 0 {
		start = 0
	}
	if start > len(page.Records) {
		start = len(page.Records)
	}
	end := len(page.Records)
	if o.Limit > 0 && start+o.Limit < end {
		end = start + o.Limit
	}
	page.Records = page.Records[start:end]
	return page, nil
}

func findFileIndex(st *jsonState, path string) int {
	for i := range st.Files {
		if st.Files[i].TrashedAt == nil && st.Files[i].LogicPath == path {
			return i
		}
	}
	return -1
}
func (s *JSONStore) UpsertFile(ctx context.Context, path, hash string, size int64) error {
	_, _, err := s.ReplaceFileConditional(ctx, path, hash, size, nil, false)
	return err
}
func (s *JSONStore) ReplaceFile(ctx context.Context, path, hash string, size int64) (string, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		old, _, err := replaceJSONFile(st, path, hash, size, nil, false)
		return old, true, err
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}
func (s *JSONStore) ReplaceFileConditional(ctx context.Context, path, hash string, size int64, expected *string, absent bool) (string, bool, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		old, matched, err := replaceJSONFile(st, path, hash, size, expected, absent)
		return struct {
			old     string
			matched bool
		}{old, matched}, matched, err
	})
	if err != nil {
		return "", false, err
	}
	r := v.(struct {
		old     string
		matched bool
	})
	return r.old, r.matched, nil
}
func replaceJSONFile(st *jsonState, path, hash string, size int64, expected *string, absent bool) (string, bool, error) {
	i := findFileIndex(st, path)
	now := time.Now().UTC()
	if i < 0 {
		if expected != nil {
			return "", false, nil
		}
		st.Files = append(st.Files, FileRecord{ID: st.NextFileID, LogicPath: path, PhysicalHash: hash, Size: size, UpdatedAt: now})
		st.NextFileID++
		return "", true, nil
	}
	if st.Files[i].IsDirectory {
		return "", false, ErrIsDirectory
	}
	old := st.Files[i].PhysicalHash
	if absent || (expected != nil && old != *expected) {
		return "", false, nil
	}
	st.Files[i].PhysicalHash = hash
	st.Files[i].Size = size
	st.Files[i].UpdatedAt = now
	if old == hash {
		old = ""
	}
	return old, true, nil
}
func (s *JSONStore) UpsertDirectory(ctx context.Context, path string) error {
	_, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findFileIndex(st, path)
		now := time.Now().UTC()
		if i < 0 {
			st.Files = append(st.Files, FileRecord{ID: st.NextFileID, LogicPath: path, IsDirectory: true, UpdatedAt: now})
			st.NextFileID++
		} else {
			st.Files[i].IsDirectory = true
			st.Files[i].UpdatedAt = now
		}
		return nil, true, nil
	})
	return err
}
func (s *JSONStore) RenamePath(ctx context.Context, from, to string) error {
	if to == from || strings.HasPrefix(to, withTrailingSlash(from)) {
		return ErrInvalidMove
	}
	_, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findFileIndex(st, from)
		if i < 0 {
			return nil, false, ErrNotFound
		}
		oldPrefix, newPrefix := withTrailingSlash(from), withTrailingSlash(to)
		targets := map[string]bool{}
		for _, r := range st.Files {
			if r.TrashedAt != nil {
				continue
			}
			if r.LogicPath == from {
				targets[to] = true
			} else if st.Files[i].IsDirectory && strings.HasPrefix(r.LogicPath, oldPrefix) {
				targets[newPrefix+strings.TrimPrefix(r.LogicPath, oldPrefix)] = true
			}
		}
		for _, r := range st.Files {
			if r.TrashedAt != nil {
				continue
			}
			if !strings.HasPrefix(r.LogicPath, oldPrefix) && r.LogicPath != from && targets[r.LogicPath] {
				return nil, false, fmt.Errorf("destination path already exists: %s", r.LogicPath)
			}
		}
		now := time.Now().UTC()
		for j := range st.Files {
			if st.Files[j].TrashedAt == nil && st.Files[j].LogicPath == from {
				st.Files[j].LogicPath = to
				st.Files[j].UpdatedAt = now
			} else if st.Files[j].TrashedAt == nil && st.Files[i].IsDirectory && strings.HasPrefix(st.Files[j].LogicPath, oldPrefix) {
				st.Files[j].LogicPath = newPrefix + strings.TrimPrefix(st.Files[j].LogicPath, oldPrefix)
				st.Files[j].UpdatedAt = now
			}
		}
		return nil, true, nil
	})
	return err
}
func (s *JSONStore) DeletePath(ctx context.Context, path string) error {
	_, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findFileIndex(st, path)
		if i < 0 {
			return nil, false, nil
		}
		st.Files = append(st.Files[:i], st.Files[i+1:]...)
		return nil, true, nil
	})
	return err
}
func (s *JSONStore) DeletePrefix(ctx context.Context, prefix string) error {
	_, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		out := st.Files[:0]
		for _, r := range st.Files {
			if r.TrashedAt != nil || !strings.HasPrefix(r.LogicPath, prefix) {
				out = append(out, r)
			}
		}
		changed := len(out) != len(st.Files)
		st.Files = out
		return nil, changed, nil
	})
	return err
}

func findShareIndex(st *jsonState, id string) int {
	for i := range st.Shares {
		if st.Shares[i].ID == id {
			return i
		}
	}
	return -1
}
func (s *JSONStore) CreateShare(ctx context.Context, r ShareRecord) (ShareRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		if findShareIndex(st, r.ID) >= 0 {
			return nil, false, fmt.Errorf("share already exists")
		}
		now := time.Now().UTC()
		if r.CreatedAt.IsZero() {
			r.CreatedAt = now
		}
		r.UpdatedAt = now
		st.Shares = append(st.Shares, r)
		return r, true, nil
	})
	if err != nil {
		return ShareRecord{}, err
	}
	return v.(ShareRecord), nil
}
func (s *JSONStore) FindShare(ctx context.Context, id string) (ShareRecord, bool, error) {
	st, err := s.read(ctx)
	if err != nil {
		return ShareRecord{}, false, err
	}
	i := findShareIndex(&st, id)
	if i < 0 {
		return ShareRecord{}, false, nil
	}
	return st.Shares[i], true, nil
}
func (s *JSONStore) updateShare(ctx context.Context, id string, fn func(*ShareRecord)) (ShareRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findShareIndex(st, id)
		if i < 0 {
			return nil, false, ErrNotFound
		}
		fn(&st.Shares[i])
		st.Shares[i].UpdatedAt = time.Now().UTC()
		return st.Shares[i], true, nil
	})
	if err != nil {
		return ShareRecord{}, err
	}
	return v.(ShareRecord), nil
}
func (s *JSONStore) MarkShareUploading(ctx context.Context, id, target string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) {
		r.Email = target
		r.Status = "uploading"
		r.Error = ""
		r.CompletedAt = nil
		r.NotifiedAt = nil
	})
}
func (s *JSONStore) MarkShareUploaded(ctx context.Context, id string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) {
		now := time.Now().UTC()
		r.Status = "completed"
		r.Error = ""
		r.CompletedAt = &now
	})
}
func (s *JSONStore) MarkShareNotified(ctx context.Context, id string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) {
		now := time.Now().UTC()
		r.Status = "notified"
		r.Error = ""
		r.NotifiedAt = &now
	})
}
func (s *JSONStore) MarkShareFailed(ctx context.Context, id, status, msg string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) { r.Status = status; r.Error = msg })
}
func (s *JSONStore) ClaimShareJob(ctx context.Context, id, owner string, until time.Time) (ShareRecord, bool, error) {
	if strings.TrimSpace(owner) == "" || !until.After(time.Now()) {
		return ShareRecord{}, false, fmt.Errorf("valid share lease owner and future expiry are required")
	}
	type claimResult struct {
		record  ShareRecord
		claimed bool
	}
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findShareIndex(st, id)
		if i < 0 {
			return claimResult{}, false, nil
		}
		r := &st.Shares[i]
		now := time.Now()
		if r.Status == "notified" || (r.ProcessingUntil != nil && r.ProcessingUntil.After(now) && (r.ProcessingBy == nil || *r.ProcessingBy != owner)) {
			return claimResult{record: *r}, false, nil
		}
		r.ProcessingBy = &owner
		r.ProcessingUntil = &until
		r.UpdatedAt = now.UTC()
		return claimResult{record: *r, claimed: true}, true, nil
	})
	if err != nil {
		return ShareRecord{}, false, err
	}
	r := v.(claimResult)
	return r.record, r.claimed, nil
}
func (s *JSONStore) ReleaseShareJob(ctx context.Context, id, owner string) error {
	_, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findShareIndex(st, id)
		if i < 0 {
			return nil, false, nil
		}
		r := &st.Shares[i]
		if r.ProcessingBy == nil || *r.ProcessingBy != owner {
			return nil, false, nil
		}
		r.ProcessingBy = nil
		r.ProcessingUntil = nil
		r.UpdatedAt = time.Now().UTC()
		return nil, true, nil
	})
	return err
}

func findUploadIndex(st *jsonState, id string) int {
	for i := range st.Uploads {
		if st.Uploads[i].ID == id {
			return i
		}
	}
	return -1
}
func (s *JSONStore) CreateUpload(ctx context.Context, r UploadRecord) (UploadRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		if findUploadIndex(st, r.ID) >= 0 {
			return nil, false, fmt.Errorf("upload already exists")
		}
		now := time.Now().UTC()
		if r.CreatedAt.IsZero() {
			r.CreatedAt = now
		}
		r.UpdatedAt = now
		st.Uploads = append(st.Uploads, r)
		return r, true, nil
	})
	if err != nil {
		return UploadRecord{}, err
	}
	return v.(UploadRecord), nil
}
func (s *JSONStore) FindUpload(ctx context.Context, id string) (UploadRecord, bool, error) {
	st, err := s.read(ctx)
	if err != nil {
		return UploadRecord{}, false, err
	}
	i := findUploadIndex(&st, id)
	if i < 0 {
		return UploadRecord{}, false, nil
	}
	return st.Uploads[i], true, nil
}
func (s *JSONStore) UpdateUpload(ctx context.Context, r UploadRecord) (UploadRecord, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findUploadIndex(st, r.ID)
		if i < 0 {
			return nil, false, ErrNotFound
		}
		r.CreatedAt = st.Uploads[i].CreatedAt
		r.UpdatedAt = time.Now().UTC()
		st.Uploads[i] = r
		return r, true, nil
	})
	if err != nil {
		return UploadRecord{}, err
	}
	return v.(UploadRecord), nil
}
func (s *JSONStore) DeleteUpload(ctx context.Context, id string) (bool, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findUploadIndex(st, id)
		if i < 0 {
			return false, false, nil
		}
		st.Uploads = append(st.Uploads[:i], st.Uploads[i+1:]...)
		return true, true, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}

func findDAVIndex(st *jsonState, token string) int {
	for i := range st.DAVLocks {
		if st.DAVLocks[i].Token == token {
			return i
		}
	}
	return -1
}
func pruneDAV(st *jsonState, now time.Time) int64 {
	out := st.DAVLocks[:0]
	var n int64
	for _, r := range st.DAVLocks {
		if !r.ExpiresAt.After(now) && (r.HeldUntil == nil || !r.HeldUntil.After(now)) {
			n++
			continue
		}
		out = append(out, r)
	}
	st.DAVLocks = out
	return n
}
func (s *JSONStore) CreateDAVLock(ctx context.Context, r DAVLockRecord) (DAVLockRecord, error) {
	r.Token = strings.TrimSpace(r.Token)
	r.Path = cleanDAVPath(r.Path)
	if r.Token == "" || (r.Depth != 0 && r.Depth != -1) || !r.ExpiresAt.After(time.Now()) {
		return DAVLockRecord{}, fmt.Errorf("invalid DAV lock")
	}
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		pruneDAV(st, time.Now())
		for _, x := range st.DAVLocks {
			if x.Token == r.Token || davLocksConflict(x, r) {
				return nil, false, ErrDAVLockConflict
			}
		}
		r.CreatedAt = time.Now().UTC()
		st.DAVLocks = append(st.DAVLocks, r)
		return r, true, nil
	})
	if err != nil {
		return DAVLockRecord{}, err
	}
	return v.(DAVLockRecord), nil
}
func (s *JSONStore) FindDAVLock(ctx context.Context, token string) (DAVLockRecord, bool, error) {
	st, err := s.read(ctx)
	if err != nil {
		return DAVLockRecord{}, false, err
	}
	i := findDAVIndex(&st, strings.TrimSpace(token))
	if i < 0 || !st.DAVLocks[i].ExpiresAt.After(time.Now()) {
		return DAVLockRecord{}, false, nil
	}
	return st.DAVLocks[i], true, nil
}
func (s *JSONStore) ListActiveDAVLocks(ctx context.Context, p string) ([]DAVLockRecord, error) {
	st, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	p = cleanDAVPath(p)
	var out []DAVLockRecord
	for _, r := range st.DAVLocks {
		if (r.ExpiresAt.After(now) || (r.HeldUntil != nil && r.HeldUntil.After(now))) && (davLockCoversPath(r, p) || davPathIsAncestor(p, r.Path)) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Token < out[j].Token
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}
func (s *JSONStore) RefreshDAVLock(ctx context.Context, token string, until time.Time) (DAVLockRecord, bool, error) {
	if !until.After(time.Now()) {
		return DAVLockRecord{}, false, fmt.Errorf("DAV lock expiry must be in the future")
	}
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findDAVIndex(st, strings.TrimSpace(token))
		if i < 0 || !st.DAVLocks[i].ExpiresAt.After(time.Now()) {
			return DAVLockRecord{}, false, nil
		}
		st.DAVLocks[i].ExpiresAt = until
		return st.DAVLocks[i], true, nil
	})
	if err != nil {
		return DAVLockRecord{}, false, err
	}
	r := v.(DAVLockRecord)
	return r, r.Token != "", nil
}
func (s *JSONStore) DeleteDAVLock(ctx context.Context, token string) (bool, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		i := findDAVIndex(st, strings.TrimSpace(token))
		if i < 0 {
			return false, false, nil
		}
		if st.DAVLocks[i].HeldUntil != nil && st.DAVLocks[i].HeldUntil.After(time.Now()) {
			return false, false, nil
		}
		st.DAVLocks = append(st.DAVLocks[:i], st.DAVLocks[i+1:]...)
		return true, true, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}
func (s *JSONStore) CleanupExpiredDAVLocks(ctx context.Context) (int64, error) {
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) { n := pruneDAV(st, time.Now()); return n, n > 0, nil })
	if err != nil {
		return 0, err
	}
	return v.(int64), nil
}
func (s *JSONStore) ClaimDAVLocks(ctx context.Context, paths, tokens []string, claim string, until time.Time) (bool, error) {
	if strings.TrimSpace(claim) == "" || !until.After(time.Now()) {
		return false, fmt.Errorf("valid DAV claim is required")
	}
	v, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		now := time.Now()
		provided := map[string]bool{}
		for _, t := range tokens {
			provided[strings.TrimSpace(t)] = true
		}
		clean := make([]string, 0, len(paths))
		for _, p := range paths {
			if strings.TrimSpace(p) != "" {
				clean = append(clean, cleanDAVPath(p))
			}
		}
		matchedPaths := make([]bool, len(clean))
		matched := map[int]bool{}
		for i := range st.DAVLocks {
			r := &st.DAVLocks[i]
			if r.HeldUntil != nil && !r.HeldUntil.After(now) {
				r.HeldBy = nil
				r.HeldUntil = nil
			}
			if !r.ExpiresAt.After(now) && (r.HeldUntil == nil || !r.HeldUntil.After(now)) {
				continue
			}
			covers := false
			for j, p := range clean {
				if davLockCoversPath(*r, p) {
					covers = true
					matchedPaths[j] = true
				}
			}
			if !covers {
				continue
			}
			if !provided[r.Token] || (r.HeldBy != nil && *r.HeldBy != claim) {
				return false, false, nil
			}
			matched[i] = true
		}
		for _, ok := range matchedPaths {
			if !ok {
				return false, false, nil
			}
		}
		for i := range matched {
			st.DAVLocks[i].HeldBy = &claim
			st.DAVLocks[i].HeldUntil = &until
		}
		return true, len(matched) > 0, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}
func (s *JSONStore) ReleaseDAVLockClaim(ctx context.Context, claim string) error {
	_, err := s.mutate(ctx, func(st *jsonState) (any, bool, error) {
		changed := false
		for i := range st.DAVLocks {
			if st.DAVLocks[i].HeldBy != nil && *st.DAVLocks[i].HeldBy == strings.TrimSpace(claim) {
				st.DAVLocks[i].HeldBy = nil
				st.DAVLocks[i].HeldUntil = nil
				changed = true
			}
		}
		return nil, changed, nil
	})
	return err
}
