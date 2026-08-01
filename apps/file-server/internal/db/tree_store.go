package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	pathpkg "path"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
)

const defaultTreePrefix = "_vfs-link-v3"
const maxGCSObjectNameBytes = 1024

func validateMetadataKey(key string) error {
	if len([]byte(key)) > maxGCSObjectNameBytes {
		return fmt.Errorf("metadata object key exceeds %d UTF-8 bytes", maxGCSObjectNameBytes)
	}
	return nil
}

// TreeStore stores each file/directory as an individual JSON object. Object
// keys mirror the logical hierarchy. Exceptionally long path segments use a
// stable hash; the complete logical path remains in the node and parent index.
type TreeStore struct {
	mu      sync.Mutex
	objects treeBackend
	prefix  string
}

var _ Store = (*TreeStore)(nil)
var _ MetadataStatsProvider = (*TreeStore)(nil)

func cleanTreePrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return defaultTreePrefix
	}
	return prefix
}

func NewTreeLocal(root, prefix string) (Store, error) {
	b, err := newLocalTreeBackend(root)
	if err != nil {
		return nil, err
	}
	return newTreeStore(b, prefix), nil
}
func NewTreeGCS(ctx context.Context, bucket, prefix string) (Store, error) {
	c, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	s, err := NewTreeGCSWithClient(c, bucket, prefix)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	s.(*TreeStore).objects.(*gcsTreeBackend).owns = true
	return s, nil
}
func NewTreeGCSWithClient(client *storage.Client, bucket, prefix string) (Store, error) {
	if client == nil {
		return nil, fmt.Errorf("GCS client is required")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("GCS metadata bucket is required")
	}
	return newTreeStore(&gcsTreeBackend{client: client, bucket: bucket}, prefix), nil
}
func newTreeStore(b treeBackend, prefix string) *TreeStore {
	prefix = cleanTreePrefix(prefix)
	return &TreeStore{objects: b, prefix: prefix}
}
func (s *TreeStore) Close() { _ = s.objects.Close() }
func (s *TreeStore) EnsureSchema(ctx context.Context) error {
	_, ok, err := s.objects.Get(ctx, s.statsKey())
	if err != nil {
		return err
	}
	if !ok {
		data, _ := json.Marshal(MetadataStats{})
		zero := int64(0)
		_, err = s.objects.Put(ctx, s.statsKey(), append(data, '\n'), &zero)
	}
	if err != nil {
		return err
	}
	_, ok, err = s.objects.Get(ctx, s.sequenceKey())
	if err != nil {
		return err
	}
	if !ok {
		b, _ := marshalTree(fileSequence{Next: 1})
		zero := int64(0)
		_, err = s.objects.Put(ctx, s.sequenceKey(), b, &zero)
	}
	return err
}

func encodeTreePath(logicPath string) string {
	logicPath = canonicalTreeIndexPath(logicPath)
	if logicPath == "" {
		return "_root"
	}
	parts := strings.Split(logicPath, "/")
	for i := range parts {
		parts[i] = encodeTreeSegment(parts[i])
	}
	return strings.Join(parts, "/")
}
func encodeTreeSegment(segment string) string {
	encoded := url.PathEscape(segment)
	if len([]byte(encoded)) <= 180 {
		return encoded
	}
	sum := sha256.Sum256([]byte(segment))
	return "~" + hex.EncodeToString(sum[:])
}
func (s *TreeStore) activeKey(path string, _ bool) string {
	return s.prefix + "/tree/nodes/" + encodeTreePath(path) + "/.vfs-node.json"
}
func (s *TreeStore) indexKey(dir string) string {
	return s.prefix + "/tree/indexes/" + encodeTreePath(dir) + ".json"
}
func (s *TreeStore) deleteIndexManifest(ctx context.Context, dir string) error {
	o, ok, e := s.objects.Get(ctx, s.indexKey(dir))
	if e != nil || !ok {
		return e
	}
	var idx directoryIndex
	if e = json.Unmarshal(o.Data, &idx); e != nil {
		return e
	}
	if e = s.objects.Delete(ctx, s.indexKey(dir), &o.Generation); e != nil {
		return e
	}
	for _, p := range idx.Pages {
		_ = s.objects.Delete(ctx, p.Key, nil)
	}
	return nil
}
func (s *TreeStore) statsKey() string    { return s.prefix + "/stats.json" }
func (s *TreeStore) sequenceKey() string { return s.prefix + "/file-sequence.json" }
func (s *TreeStore) trashPrefix(id string) string {
	return s.prefix + "/trash/" + encodeTreeSegment(id) + "/"
}
func (s *TreeStore) trashNodeKey(id string, r FileRecord) string {
	kind := "files"
	if r.IsDirectory {
		kind = "directories"
	}
	return s.trashPrefix(id) + kind + "/" + encodeTreePath(r.LogicPath) + ".json"
}

type fileSequence struct {
	Next int `json:"next"`
}
type treeMutationLease struct {
	Owner string    `json:"owner"`
	Until time.Time `json:"until"`
}

func (s *TreeStore) acquireTreeMutationLease(ctx context.Context) (func(), func(context.Context) error, error) {
	key := s.prefix + "/leases/tree-mutation.json"
	owner := uuid.NewString()
	for attempt := 0; attempt < treeCASAttempts*4; attempt++ {
		o, ok, e := s.objects.Get(ctx, key)
		if e != nil {
			return nil, nil, e
		}
		lease := treeMutationLease{Owner: owner, Until: time.Now().UTC().Add(30 * time.Minute)}
		b, _ := marshalTree(lease)
		g := o.Generation
		if !ok {
			g = 0
		} else {
			var current treeMutationLease
			if e = json.Unmarshal(o.Data, &current); e != nil {
				return nil, nil, e
			}
			if current.Until.After(time.Now()) {
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-time.After(10 * time.Millisecond):
					continue
				}
			}
		}
		newGen, e := s.objects.Put(ctx, key, b, &g)
		if e == nil {
			var leaseMu sync.Mutex
			generation := newGen
			renew := func(renewCtx context.Context) error {
				leaseMu.Lock()
				defer leaseMu.Unlock()
				o, exists, e := s.objects.Get(renewCtx, key)
				if e != nil {
					return e
				}
				if !exists {
					return fmt.Errorf("tree mutation lease lost")
				}
				var current treeMutationLease
				if e = json.Unmarshal(o.Data, &current); e != nil {
					return e
				}
				if current.Owner != owner {
					return fmt.Errorf("tree mutation lease lost")
				}
				current.Until = time.Now().UTC().Add(30 * time.Minute)
				b, _ := marshalTree(current)
				next, e := s.objects.Put(renewCtx, key, b, &o.Generation)
				if e == nil {
					generation = next
				}
				return e
			}
			release := func() {
				leaseMu.Lock()
				defer leaseMu.Unlock()
				_ = s.objects.Delete(context.Background(), key, &generation)
			}
			return release, renew, nil
		}
		if !errorsIsConflict(e) {
			return nil, nil, e
		}
	}
	return nil, nil, ErrMetadataConflict
}

func (s *TreeStore) nextID(ctx context.Context) (int, error) {
	for n := 0; n < treeCASAttempts; n++ {
		o, ok, e := s.objects.Get(ctx, s.sequenceKey())
		if e != nil {
			return 0, e
		}
		seq := fileSequence{Next: 1}
		if ok {
			if e = json.Unmarshal(o.Data, &seq); e != nil {
				return 0, e
			}
		}
		if seq.Next < 1 {
			seq.Next = 1
		}
		id := seq.Next
		seq.Next++
		b, _ := marshalTree(seq)
		g := o.Generation
		if !ok {
			g = 0
		}
		if _, e = s.objects.Put(ctx, s.sequenceKey(), b, &g); e == nil {
			return id, nil
		} else if !errorsIsConflict(e) {
			return 0, e
		}
	}
	return 0, ErrMetadataConflict
}
func normalizeTreeRecord(r FileRecord) FileRecord {
	r.LogicPath = cleanLogicPath(r.LogicPath)
	return r
}

func canonicalTreeIndexPath(logicPath string) string {
	return cleanLogicPath(logicPath)
}
func marshalTree(v any) ([]byte, error) {
	b, e := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), e
}
func decodeTreeRecord(o treeObject) (FileRecord, error) {
	var r FileRecord
	e := json.Unmarshal(o.Data, &r)
	return normalizeTreeRecord(r), e
}

const directoryIndexPageSize = 256

type indexPageDescriptor struct {
	Key          string `json:"key"`
	Count        int    `json:"count"`
	First        string `json:"first"`
	Last         string `json:"last"`
	DirectBytes  int64  `json:"directBytes,omitempty"`
	SubtreeFiles int64  `json:"subtreeFiles,omitempty"`
	SubtreeDirs  int64  `json:"subtreeDirectories,omitempty"`
	SubtreeBytes int64  `json:"subtreeBytes,omitempty"`
}
type directoryIndex struct {
	Version          int                   `json:"version"`
	AggregateVersion int                   `json:"aggregateVersion,omitempty"`
	Directory        string                `json:"directory"`
	Records          []FileRecord          `json:"-"`
	Pages            []indexPageDescriptor `json:"pages"`
	Total            int                   `json:"total"`
	TotalBytes       int64                 `json:"totalBytes"`
	DirectBytes      int64                 `json:"directBytes,omitempty"`
	SubtreeFiles     int64                 `json:"subtreeFiles,omitempty"`
	SubtreeDirs      int64                 `json:"subtreeDirectories,omitempty"`
	SubtreeBytes     int64                 `json:"subtreeBytes,omitempty"`
	UpdatedAt        time.Time             `json:"updatedAt"`
}
type directoryIndexPage struct {
	Records []FileRecord `json:"records"`
}

func indexSortKey(r FileRecord) string {
	kind := "1"
	if r.IsDirectory {
		kind = "0"
	}
	return kind + "\x00" + pathpkg.Base(r.LogicPath)
}
func (s *TreeStore) indexPageKey(dir string) string {
	return s.prefix + "/tree/index-pages/" + encodeTreePath(dir) + "/" + fmt.Sprintf("%d", time.Now().UnixNano()) + "-" + url.PathEscape(uuid.NewString()) + ".json"
}

func (s *TreeStore) getIndex(ctx context.Context, dir string) (directoryIndex, int64, bool, error) {
	idx, g, ok, e := s.getIndexManifest(ctx, dir)
	if e != nil || !ok {
		return idx, g, ok, e
	}
	for _, p := range idx.Pages {
		page, e := s.loadIndexPage(ctx, p.Key)
		if e != nil {
			return idx, 0, true, e
		}
		idx.Records = append(idx.Records, page.Records...)
	}
	return idx, g, true, nil
}
func (s *TreeStore) getIndexManifest(ctx context.Context, dir string) (directoryIndex, int64, bool, error) {
	o, ok, e := s.objects.Get(ctx, s.indexKey(dir))
	if e != nil || !ok {
		return directoryIndex{Version: 2, AggregateVersion: 2, Directory: dir}, 0, ok, e
	}
	var idx directoryIndex
	e = json.Unmarshal(o.Data, &idx)
	return idx, o.Generation, true, e
}
func (s *TreeStore) loadIndexPage(ctx context.Context, key string) (directoryIndexPage, error) {
	o, ok, e := s.objects.Get(ctx, key)
	if e != nil {
		return directoryIndexPage{}, e
	}
	if !ok {
		return directoryIndexPage{}, fmt.Errorf("index page missing: %s", key)
	}
	var p directoryIndexPage
	e = json.Unmarshal(o.Data, &p)
	return p, e
}
func (s *TreeStore) putIndexPage(ctx context.Context, dir string, records []FileRecord) (indexPageDescriptor, error) {
	sort.Slice(records, func(i, j int) bool { return indexSortKey(records[i]) < indexSortKey(records[j]) })
	key := s.indexPageKey(dir)
	if e := validateMetadataKey(key); e != nil {
		return indexPageDescriptor{}, e
	}
	b, _ := marshalTree(directoryIndexPage{Records: records})
	if _, e := s.objects.Put(ctx, key, b, nil); e != nil {
		return indexPageDescriptor{}, e
	}
	d := summarizeIndexPage(records)
	d.Key = key
	d.Count = len(records)
	d.First = indexSortKey(records[0])
	d.Last = indexSortKey(records[len(records)-1])
	return d, nil
}
func (s *TreeStore) updateIndexRecord(ctx context.Context, dir string, record FileRecord, remove bool) error {
	return s.updateIndexRecordLeaseHeld(ctx, dir, record, remove, false)
}

// updateIndexRecordLeaseHeld updates one parent index. Callers that already
// own the distributed tree mutation lease may request absolute propagation to
// root. Bulk operations suppress it while rewriting individual nodes and call
// propagateDirectorySummaryLeaseHeld once their tree is stable.
func (s *TreeStore) updateIndexRecordLeaseHeld(ctx context.Context, dir string, record FileRecord, remove, propagate bool) error {
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		idx, g, exists, e := s.getIndexManifest(ctx, dir)
		if e != nil {
			return e
		}
		key := indexSortKey(record)
		pi := len(idx.Pages) - 1
		for i, p := range idx.Pages {
			if key <= p.Last {
				pi = i
				break
			}
		}
		var records []FileRecord
		var oldKey string
		if pi >= 0 {
			oldKey = idx.Pages[pi].Key
			page, e := s.loadIndexPage(ctx, oldKey)
			if e != nil {
				return e
			}
			records = page.Records
		}
		beforeCount := len(records)
		if !remove {
			if record.IsDirectory {
				hydrated, hydrateErr := s.hydrateDirectorySummaries(ctx, []FileRecord{record})
				if hydrateErr != nil {
					return hydrateErr
				}
				record = hydrated[0]
			}
			records = upsertIndexRecord(records, record)
		} else {
			records = removeIndexRecord(records, record.LogicPath)
		}
		var replacements []indexPageDescriptor
		if len(records) > 0 {
			for start := 0; start < len(records); start += directoryIndexPageSize {
				end := start + directoryIndexPageSize
				if end > len(records) {
					end = len(records)
				}
				d, e := s.putIndexPage(ctx, dir, append([]FileRecord(nil), records[start:end]...))
				if e != nil {
					return e
				}
				replacements = append(replacements, d)
			}
		}
		if pi >= 0 {
			idx.Pages = append(append(append([]indexPageDescriptor{}, idx.Pages[:pi]...), replacements...), idx.Pages[pi+1:]...)
		} else {
			idx.Pages = replacements
		}
		idx.Total += len(records) - beforeCount
		summarizeIndexManifest(&idx)
		idx.UpdatedAt = time.Now().UTC()
		b, _ := marshalTree(idx)
		if !exists {
			g = 0
		}
		if _, e = s.objects.Put(ctx, s.indexKey(dir), b, &g); e == nil {
			if oldKey != "" {
				_ = s.objects.Delete(ctx, oldKey, nil)
			}
			if propagate {
				return s.propagateDirectorySummaryLeaseHeld(ctx, dir)
			}
			return nil
		} else if !errorsIsConflict(e) {
			return e
		}
	}
	return ErrMetadataConflict
}
func (s *TreeStore) writeIndex(ctx context.Context, idx directoryIndex, g int64, exists bool) error {
	if e := validateMetadataKey(s.indexKey(idx.Directory)); e != nil {
		return e
	}
	sort.Slice(idx.Records, func(i, j int) bool { return indexSortKey(idx.Records[i]) < indexSortKey(idx.Records[j]) })
	old := idx.Pages
	idx.Pages = nil
	idx.Total = len(idx.Records)
	var e error
	idx.Records, e = s.hydrateDirectorySummaries(ctx, idx.Records)
	if e != nil {
		return e
	}
	for start := 0; start < len(idx.Records); start += directoryIndexPageSize {
		end := start + directoryIndexPageSize
		if end > len(idx.Records) {
			end = len(idx.Records)
		}
		d, e := s.putIndexPage(ctx, idx.Directory, append([]FileRecord(nil), idx.Records[start:end]...))
		if e != nil {
			return e
		}
		idx.Pages = append(idx.Pages, d)
	}
	idx.Records = nil
	summarizeIndexManifest(&idx)
	idx.UpdatedAt = time.Now().UTC()
	b, _ := marshalTree(idx)
	if !exists {
		g = 0
	}
	if _, e := s.objects.Put(ctx, s.indexKey(idx.Directory), b, &g); e != nil {
		return e
	}
	for _, p := range old {
		_ = s.objects.Delete(ctx, p.Key, nil)
	}
	return nil
}
func errorsIsConflict(err error) bool {
	return err == ErrMetadataConflict || strings.Contains(err.Error(), ErrMetadataConflict.Error())
}
func upsertIndexRecord(records []FileRecord, r FileRecord) []FileRecord {
	for i := range records {
		if records[i].LogicPath == r.LogicPath {
			records[i] = r
			return records
		}
	}
	return append(records, r)
}
func removeIndexRecord(records []FileRecord, path string) []FileRecord {
	out := records[:0]
	for _, r := range records {
		if r.LogicPath != path {
			out = append(out, r)
		}
	}
	return out
}

func (s *TreeStore) Find(ctx context.Context, path string) (FileRecord, bool, error) {
	var err error
	path, err = parseLogicPath(path)
	if err != nil {
		return FileRecord{}, false, err
	}
	if path == "" {
		return FileRecord{}, false, nil
	}
	key := s.activeKey(path, false)
	if e := validateMetadataKey(key); e != nil {
		return FileRecord{}, false, e
	}
	o, ok, e := s.objects.Get(ctx, key)
	if e != nil {
		return FileRecord{}, false, e
	}
	if ok {
		r, e := decodeTreeRecord(o)
		return r, true, e
	}
	return FileRecord{}, false, nil
}
func (s *TreeStore) ListDirectChildren(ctx context.Context, dir string, o DirectChildrenOptions) (DirectChildrenPage, error) {
	var err error
	dir, err = parseLogicPath(dir)
	if err != nil {
		return DirectChildrenPage{}, err
	}
	idx, _, ok, e := s.getIndexManifest(ctx, dir)
	if e != nil {
		return DirectChildrenPage{}, e
	}
	if !ok {
		return DirectChildrenPage{}, nil
	}
	q := strings.ToLower(strings.TrimSpace(o.Query))
	var records []FileRecord
	// Normal pagination reads only overlapping immutable pages. Queries and
	// directory-only pickers scan pages because their filtered cardinality is
	// not represented by the generic descriptor.
	if q == "" && !o.DirectoriesOnly {
		start := o.Offset
		if start < 0 {
			start = 0
		}
		need := o.Limit
		if need <= 0 {
			need = idx.Total - start
		}
		cursor := 0
		for _, d := range idx.Pages {
			if cursor+d.Count <= start {
				cursor += d.Count
				continue
			}
			page, e := s.loadIndexPage(ctx, d.Key)
			if e != nil {
				return DirectChildrenPage{}, e
			}
			from := 0
			if start > cursor {
				from = start - cursor
			}
			take := len(page.Records) - from
			if take > need {
				take = need
			}
			if take > 0 {
				records = append(records, page.Records[from:from+take]...)
				need -= take
			}
			cursor += d.Count
			if need <= 0 {
				break
			}
		}
	} else {
		for _, d := range idx.Pages {
			page, e := s.loadIndexPage(ctx, d.Key)
			if e != nil {
				return DirectChildrenPage{}, e
			}
			records = append(records, page.Records...)
		}
	}
	p := DirectChildrenPage{FolderSummary: folderSummaryFromIndex(idx)}
	for _, r := range records {
		if o.DirectoriesOnly && !r.IsDirectory {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(pathpkg.Base(r.LogicPath)), q) {
			continue
		}
		p.Records = append(p.Records, r)
	}
	if q == "" && !o.DirectoriesOnly {
		p.Total = idx.Total
		p.TotalBytes = idx.TotalBytes
		return p, nil
	}
	p.Total = len(p.Records)
	for _, r := range p.Records {
		if !r.IsDirectory {
			p.TotalBytes += r.Size
		}
	}
	start := o.Offset
	if start < 0 {
		start = 0
	}
	if start > len(p.Records) {
		start = len(p.Records)
	}
	end := len(p.Records)
	if o.Limit > 0 && start+o.Limit < end {
		end = start + o.Limit
	}
	p.Records = p.Records[start:end]
	return p, nil
}
func (s *TreeStore) listActive(ctx context.Context, prefix string) ([]FileRecord, error) {
	var out []FileRecord
	for _, kind := range []string{"nodes"} {
		keys, e := s.objects.List(ctx, s.prefix+"/tree/"+kind+"/")
		if e != nil {
			return nil, e
		}
		for _, key := range keys {
			o, ok, e := s.objects.Get(ctx, key)
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
			r, e := decodeTreeRecord(o)
			if e != nil {
				return nil, e
			}
			if prefix == "" || strings.HasPrefix(r.LogicPath, prefix) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LogicPath < out[j].LogicPath })
	return out, nil
}
func (s *TreeStore) ListAll(ctx context.Context) ([]FileRecord, error) { return s.listActive(ctx, "") }
func (s *TreeStore) ListPrefix(ctx context.Context, prefix string) ([]FileRecord, error) {
	var err error
	prefix, err = parseLogicPrefix(prefix)
	if err != nil {
		return nil, err
	}
	return s.listActive(ctx, prefix)
}

func (s *TreeStore) putNode(ctx context.Context, r FileRecord, requireAbsent bool) error {
	r = normalizeTreeRecord(r)
	// Aggregate snapshots belong to parent index entries, never canonical
	// file/directory marker nodes.
	r.FolderSummary = nil
	key := s.activeKey(r.LogicPath, r.IsDirectory)
	if e := validateMetadataKey(key); e != nil {
		return e
	}
	data, e := marshalTree(r)
	if e != nil {
		return e
	}
	var expected *int64
	if requireAbsent {
		z := int64(0)
		expected = &z
	}
	_, e = s.objects.Put(ctx, key, data, expected)
	return e
}
func (s *TreeStore) deleteNode(ctx context.Context, r FileRecord) error {
	return s.objects.Delete(ctx, s.activeKey(r.LogicPath, r.IsDirectory), nil)
}
func (s *TreeStore) UpsertFile(ctx context.Context, path, hash string, size int64) error {
	_, _, e := s.ReplaceFileConditional(ctx, path, hash, size, nil, false)
	return e
}
func (s *TreeStore) ReplaceFile(ctx context.Context, path, hash string, size int64) (string, error) {
	old, _, e := s.ReplaceFileConditional(ctx, path, hash, size, nil, false)
	return old, e
}
func (s *TreeStore) ReplaceFileConditional(ctx context.Context, path, hash string, size int64, expected *string, absent bool) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, _, e := s.acquireTreeMutationLease(ctx)
	if e != nil {
		return "", false, e
	}
	defer release()
	path, e = parseLogicPath(path)
	if e != nil {
		return "", false, e
	}
	if path == "" {
		return "", false, fmt.Errorf("file path is required")
	}
	old, ok, e := s.Find(ctx, path)
	if e != nil {
		return "", false, e
	}
	if ok && old.IsDirectory {
		return "", false, ErrIsDirectory
	}
	if !ok && expected != nil {
		return "", false, nil
	}
	if ok && (absent || (expected != nil && old.PhysicalHash != *expected)) {
		return "", false, nil
	}
	now := time.Now().UTC()
	r := FileRecord{LogicPath: path, PhysicalHash: hash, Size: size, UpdatedAt: now}
	if ok {
		r.ID = old.ID
	} else if r.ID, e = s.nextID(ctx); e != nil {
		return "", false, e
	}
	if e = s.putNode(ctx, r, !ok); e != nil {
		return "", false, e
	}
	if e = s.updateIndexRecordLeaseHeld(ctx, parentLogicPath(path), r, false, true); e != nil {
		return "", false, e
	}
	delta := MetadataStats{}
	if !ok {
		delta.LogicalFiles = 1
		delta.LogicalBytes = size
		delta.PhysicalObjects = 1
		delta.PhysicalBytes = size
	} else {
		delta.LogicalBytes = size - old.Size
		delta.PhysicalBytes = size - old.Size
	}
	if e = s.mutateStats(ctx, delta); e != nil {
		return "", false, e
	}
	oldHash := ""
	if ok && old.PhysicalHash != hash {
		oldHash = old.PhysicalHash
	}
	return oldHash, true, nil
}
func (s *TreeStore) UpsertDirectory(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, _, e := s.acquireTreeMutationLease(ctx)
	if e != nil {
		return e
	}
	defer release()
	path, e = parseLogicPath(path)
	if e != nil {
		return e
	}
	if path == "" {
		return nil
	}
	old, ok, e := s.Find(ctx, path)
	if e != nil {
		return e
	}
	if ok && !old.IsDirectory {
		return ErrPathConflict
	}
	r := FileRecord{LogicPath: path, IsDirectory: true, UpdatedAt: time.Now().UTC()}
	if ok {
		r.ID = old.ID
	} else if r.ID, e = s.nextID(ctx); e != nil {
		return e
	}
	if e = s.putNode(ctx, r, !ok); e != nil {
		return e
	}
	_, _, exists, e := s.getIndex(ctx, path)
	if e == nil && !exists {
		idx := directoryIndex{Version: 2, AggregateVersion: directoryAggregateVersion, Directory: path, UpdatedAt: time.Now().UTC()}
		e = s.writeIndex(ctx, idx, 0, false)
	}
	if e == nil {
		e = s.updateIndexRecordLeaseHeld(ctx, parentLogicPath(path), r, false, true)
	}
	if e == nil && !ok {
		e = s.mutateStats(ctx, MetadataStats{LogicalDirs: 1})
	}
	return e
}
func (s *TreeStore) DeletePath(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, _, e := s.acquireTreeMutationLease(ctx)
	if e != nil {
		return e
	}
	defer release()
	r, ok, e := s.Find(ctx, path)
	if e != nil || !ok {
		return e
	}
	if e = s.deleteNode(ctx, r); e != nil {
		return e
	}
	if e = s.updateIndexRecordLeaseHeld(ctx, parentLogicPath(r.LogicPath), r, true, true); e != nil {
		return e
	}
	if r.IsDirectory {
		_ = s.objects.Delete(ctx, s.indexKey(r.LogicPath), nil)
	}
	delta := MetadataStats{}
	if r.IsDirectory {
		delta.LogicalDirs = -1
	} else {
		delta.LogicalFiles = -1
		delta.LogicalBytes = -r.Size
		delta.PhysicalObjects = -1
		delta.PhysicalBytes = -r.Size
	}
	return s.mutateStats(ctx, delta)
}
func (s *TreeStore) DeletePrefix(ctx context.Context, prefix string) error {
	records, e := s.ListPrefix(ctx, prefix)
	if e != nil {
		return e
	}
	for _, r := range records {
		if e = s.DeletePath(ctx, r.LogicPath); e != nil {
			return e
		}
	}
	return nil
}
func (s *TreeStore) MetadataStats(ctx context.Context) (MetadataStats, error) {
	o, ok, e := s.objects.Get(ctx, s.statsKey())
	if e != nil {
		return MetadataStats{}, e
	}
	if !ok {
		return MetadataStats{}, nil
	}
	var st MetadataStats
	e = json.Unmarshal(o.Data, &st)
	return st, e
}
func (s *TreeStore) mutateStats(ctx context.Context, d MetadataStats) error {
	for n := 0; n < treeCASAttempts; n++ {
		o, ok, e := s.objects.Get(ctx, s.statsKey())
		if e != nil {
			return e
		}
		var st MetadataStats
		if ok {
			if e = json.Unmarshal(o.Data, &st); e != nil {
				return e
			}
		}
		st.LogicalFiles += d.LogicalFiles
		st.LogicalDirs += d.LogicalDirs
		st.LogicalBytes += d.LogicalBytes
		st.PhysicalObjects += d.PhysicalObjects
		st.PhysicalBytes += d.PhysicalBytes
		b, _ := marshalTree(st)
		g := o.Generation
		if !ok {
			g = 0
		}
		if _, e = s.objects.Put(ctx, s.statsKey(), b, &g); e == nil {
			return nil
		} else if !errorsIsConflict(e) {
			return e
		}
	}
	return ErrMetadataConflict
}
func (s *TreeStore) mutateStatsOnce(ctx context.Context, token string, d MetadataStats) error {
	for n := 0; n < treeCASAttempts; n++ {
		o, ok, e := s.objects.Get(ctx, s.statsKey())
		if e != nil {
			return e
		}
		var st MetadataStats
		if ok {
			if e = json.Unmarshal(o.Data, &st); e != nil {
				return e
			}
		}
		for _, id := range st.AppliedOperationIDs {
			if id == token {
				return nil
			}
		}
		st.LogicalFiles += d.LogicalFiles
		st.LogicalDirs += d.LogicalDirs
		st.LogicalBytes += d.LogicalBytes
		st.PhysicalObjects += d.PhysicalObjects
		st.PhysicalBytes += d.PhysicalBytes
		st.AppliedOperationIDs = append(st.AppliedOperationIDs, token)
		b, _ := marshalTree(st)
		g := o.Generation
		if !ok {
			g = 0
		}
		if _, e = s.objects.Put(ctx, s.statsKey(), b, &g); e == nil {
			return nil
		} else if !errorsIsConflict(e) {
			return e
		}
	}
	return ErrMetadataConflict
}
func (s *TreeStore) removeStatsToken(ctx context.Context, token string) error {
	for n := 0; n < treeCASAttempts; n++ {
		o, ok, e := s.objects.Get(ctx, s.statsKey())
		if e != nil || !ok {
			return e
		}
		var st MetadataStats
		if e = json.Unmarshal(o.Data, &st); e != nil {
			return e
		}
		out := st.AppliedOperationIDs[:0]
		for _, id := range st.AppliedOperationIDs {
			if id != token {
				out = append(out, id)
			}
		}
		if len(out) == len(st.AppliedOperationIDs) {
			return nil
		}
		st.AppliedOperationIDs = out
		b, _ := marshalTree(st)
		if _, e = s.objects.Put(ctx, s.statsKey(), b, &o.Generation); e == nil {
			return nil
		} else if !errorsIsConflict(e) {
			return e
		}
	}
	return ErrMetadataConflict
}

type TreeImportSnapshot struct {
	Records          []FileRecord
	Shares           []ShareRecord
	DAVLocks         []DAVLockRecord
	Uploads          []UploadRecord
	Thumbnails       []ThumbnailRecord
	ThumbnailLinks   []FileThumbnailLink
	NextFileID       int
	SourceSHA256     string
	SourceGeneration int64
}
type treeImportManifest struct {
	Version          int       `json:"version"`
	SourceSHA256     string    `json:"sourceSha256"`
	SourceGeneration int64     `json:"sourceGeneration"`
	Records          int       `json:"records"`
	Shares           int       `json:"shares"`
	DAVLocks         int       `json:"davLocks"`
	Uploads          int       `json:"uploads"`
	Thumbnails       int       `json:"thumbnails"`
	Active           int       `json:"active"`
	Trash            int       `json:"trash"`
	Files            int       `json:"files"`
	Directories      int       `json:"directories"`
	Bytes            int64     `json:"bytes"`
	ImportedAt       time.Time `json:"importedAt"`
}

func ValidateTreeImport(prefix string, snapshot TreeImportSnapshot) (TreeValidation, error) {
	prefix = cleanTreePrefix(prefix)
	fake := &TreeStore{prefix: prefix}
	seen := map[string]bool{}
	validateUniqueKey := func(key string) error {
		if e := validateMetadataKey(key); e != nil {
			return e
		}
		if seen[key] {
			return fmt.Errorf("duplicate metadata key: %s", key)
		}
		seen[key] = true
		return nil
	}
	v := TreeValidation{Expected: len(snapshot.Records), Actual: len(snapshot.Records)}
	dirs := map[string]bool{"": true}
	trashRoots := map[string]bool{}
	fileIDs := map[int]bool{}
	for _, r := range snapshot.Records {
		r = normalizeTreeRecord(r)
		var key string
		if r.TrashedAt != nil {
			if r.TrashID == "" {
				return v, fmt.Errorf("trashed record %s has no trash id", r.LogicPath)
			}
			key = fake.trashNodeKey(r.TrashID, r)
			v.Trash++
		} else {
			key = fake.activeKey(r.LogicPath, r.IsDirectory)
			v.Active++
			dirs[parentLogicPath(r.LogicPath)] = true
			if r.IsDirectory {
				dirs[canonicalTreeIndexPath(r.LogicPath)] = true
			}
		}
		if e := validateUniqueKey(key); e != nil {
			return v, e
		}
		if r.TrashedAt != nil && r.TrashRoot {
			if trashRoots[r.TrashID] {
				return v, fmt.Errorf("duplicate trash root: %s", r.TrashID)
			}
			trashRoots[r.TrashID] = true
		}
		if r.IsDirectory {
			v.Directories++
		} else {
			v.Files++
			v.Bytes += r.Size
			fileIDs[r.ID] = true
		}
	}
	for dir := range dirs {
		if e := validateUniqueKey(fake.indexKey(dir)); e != nil {
			return v, e
		}
		sample := prefix + "/tree/index-pages/" + encodeTreePath(dir) + "/0000000000000000000-00000000-0000-0000-0000-000000000000.json"
		if e := validateMetadataKey(sample); e != nil {
			return v, e
		}
	}
	for id := range trashRoots {
		if e := validateUniqueKey(fake.trashManifestKey(id)); e != nil {
			return v, e
		}
	}
	for _, share := range snapshot.Shares {
		if e := validateUniqueKey(fake.entityKey("shares", share.ID)); e != nil {
			return v, e
		}
	}
	for _, lock := range snapshot.DAVLocks {
		if e := validateUniqueKey(fake.entityKey("dav-locks", lock.Token)); e != nil {
			return v, e
		}
	}
	for _, upload := range snapshot.Uploads {
		if e := validateUniqueKey(fake.entityKey("uploads", upload.ID)); e != nil {
			return v, e
		}
	}
	for _, thumbnail := range snapshot.Thumbnails {
		if e := validateUniqueKey(fake.entityKey("thumbnails", thumbnail.ID)); e != nil {
			return v, e
		}
		for _, fileID := range thumbnail.FileIDs {
			if !fileIDs[fileID] {
				return v, fmt.Errorf("thumbnail %s references unknown file id %d", thumbnail.ID, fileID)
			}
		}
	}
	thumbnailIDs := make(map[string]bool, len(snapshot.Thumbnails))
	for _, thumbnail := range snapshot.Thumbnails {
		thumbnailIDs[thumbnail.ID] = true
	}
	linkedFiles := make(map[int]bool, len(snapshot.ThumbnailLinks))
	for _, link := range snapshot.ThumbnailLinks {
		if link.FileID <= 0 || !fileIDs[link.FileID] {
			return v, fmt.Errorf("thumbnail link references unknown file id %d", link.FileID)
		}
		if !thumbnailIDs[link.ThumbnailID] {
			return v, fmt.Errorf("thumbnail link for file id %d references unknown thumbnail %s", link.FileID, link.ThumbnailID)
		}
		if linkedFiles[link.FileID] {
			return v, fmt.Errorf("duplicate thumbnail link for file id %d", link.FileID)
		}
		linkedFiles[link.FileID] = true
		if e := validateUniqueKey(fake.entityKey(fileThumbnailEntityKind, fileThumbnailEntityID(link.FileID))); e != nil {
			return v, e
		}
	}
	return v, nil
}

type TreeValidation struct {
	Expected    int   `json:"expected"`
	Actual      int   `json:"actual"`
	Active      int   `json:"active"`
	Trash       int   `json:"trash"`
	Files       int   `json:"files"`
	Directories int   `json:"directories"`
	Bytes       int64 `json:"bytes"`
}

func BulkImportTree(ctx context.Context, store Store, snapshot TreeImportSnapshot) (TreeValidation, error) {
	tree, ok := store.(*TreeStore)
	if !ok {
		return TreeValidation{}, fmt.Errorf("tree store is required")
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if _, e := ValidateTreeImport(tree.prefix, snapshot); e != nil {
		return TreeValidation{}, e
	}
	if keys, e := tree.objects.List(ctx, tree.prefix+"/"); e != nil {
		return TreeValidation{}, e
	} else {
		for _, key := range keys {
			if key == tree.statsKey() || key == tree.sequenceKey() {
				continue
			}
			return TreeValidation{}, fmt.Errorf("tree import destination is not empty: %s", key)
		}
	}
	byDir := map[string][]FileRecord{"": nil}
	trashRoots := map[string]FileRecord{}
	physical := map[string]int64{}
	var st MetadataStats
	maxID := 0
	tasks := make([]func(context.Context) error, 0, len(snapshot.Records))
	validation := TreeValidation{Expected: len(snapshot.Records), Actual: len(snapshot.Records)}
	seenKeys := map[string]bool{}
	for _, r := range snapshot.Records {
		r = normalizeTreeRecord(r)
		if r.IsDirectory {
			validation.Directories++
		} else {
			validation.Files++
			validation.Bytes += r.Size
		}
		if r.ID > maxID {
			maxID = r.ID
		}
		if r.TrashedAt != nil {
			if r.TrashID == "" {
				return TreeValidation{}, fmt.Errorf("trashed record %s has no trash id", r.LogicPath)
			}
			key := tree.trashNodeKey(r.TrashID, r)
			if e := validateMetadataKey(key); e != nil {
				return TreeValidation{}, e
			}
			if seenKeys[key] {
				return TreeValidation{}, fmt.Errorf("duplicate metadata key: %s", key)
			}
			seenKeys[key] = true
			rCopy, keyCopy := r, key
			tasks = append(tasks, func(taskCtx context.Context) error {
				b, _ := marshalTree(rCopy)
				z := int64(0)
				_, e := tree.objects.Put(taskCtx, keyCopy, b, &z)
				return e
			})
			validation.Trash++
			if r.TrashRoot {
				trashRoots[r.TrashID] = r
			}
			continue
		}
		activeKey := tree.activeKey(r.LogicPath, r.IsDirectory)
		if e := validateMetadataKey(activeKey); e != nil {
			return TreeValidation{}, e
		}
		if seenKeys[activeKey] {
			return TreeValidation{}, fmt.Errorf("duplicate metadata key: %s", activeKey)
		}
		seenKeys[activeKey] = true
		rCopy := r
		tasks = append(tasks, func(taskCtx context.Context) error { return tree.putNode(taskCtx, rCopy, true) })
		validation.Active++
		parentDir := parentLogicPath(r.LogicPath)
		byDir[parentDir] = append(byDir[parentDir], r)
		if r.IsDirectory {
			// Empty directories also need an aggregate manifest so their parent
			// can publish an explicit zero summary.
			directory := canonicalTreeIndexPath(r.LogicPath)
			if _, exists := byDir[directory]; !exists {
				byDir[directory] = nil
			}
			st.LogicalDirs++
		} else {
			st.LogicalFiles++
			st.LogicalBytes += r.Size
			physical[r.PhysicalHash] = r.Size
		}
	}
	for dir := range byDir {
		if e := validateMetadataKey(tree.indexKey(dir)); e != nil {
			return TreeValidation{}, e
		}
		sample := tree.prefix + "/tree/index-pages/" + encodeTreePath(dir) + "/0000000000000000000-00000000-0000-0000-0000-000000000000.json"
		if e := validateMetadataKey(sample); e != nil {
			return TreeValidation{}, e
		}
	}
	if e := runTreeImportTasks(ctx, 32, tasks); e != nil {
		return TreeValidation{}, e
	}
	for _, r := range snapshot.Shares {
		rCopy := r
		tasks = append(tasks, func(taskCtx context.Context) error { return tree.putEntity(taskCtx, "shares", rCopy.ID, rCopy, true) })
	}
	for _, r := range snapshot.DAVLocks {
		rCopy := r
		tasks = append(tasks, func(taskCtx context.Context) error {
			return tree.putEntity(taskCtx, "dav-locks", rCopy.Token, rCopy, true)
		})
	}
	for _, r := range snapshot.Uploads {
		rCopy := r
		tasks = append(tasks, func(taskCtx context.Context) error { return tree.putEntity(taskCtx, "uploads", rCopy.ID, rCopy, true) })
	}
	for _, r := range snapshot.Thumbnails {
		rCopy := r
		tasks = append(tasks, func(taskCtx context.Context) error {
			return tree.putEntity(taskCtx, "thumbnails", rCopy.ID, rCopy, true)
		})
	}
	// Direct links are canonical in new snapshots. For legacy snapshots which
	// have only ThumbnailRecord.FileIDs, derive the equivalent link entities at
	// import time. Explicit links always win in mixed snapshots.
	linksByFile := make(map[int]FileThumbnailLink, len(snapshot.ThumbnailLinks))
	for _, link := range snapshot.ThumbnailLinks {
		linksByFile[link.FileID] = link
	}
	legacyByFile := make(map[int]ThumbnailRecord)
	for _, thumbnail := range snapshot.Thumbnails {
		for _, fileID := range normalizeFileIDs(thumbnail.FileIDs) {
			current, exists := legacyByFile[fileID]
			if !exists || thumbnail.CreatedAt.After(current.CreatedAt) || (thumbnail.CreatedAt.Equal(current.CreatedAt) && thumbnail.ID > current.ID) {
				legacyByFile[fileID] = thumbnail
			}
		}
	}
	for fileID, thumbnail := range legacyByFile {
		if _, exists := linksByFile[fileID]; !exists {
			linksByFile[fileID] = FileThumbnailLink{FileID: fileID, ThumbnailID: thumbnail.ID, UpdatedAt: time.Now().UTC()}
		}
	}
	for _, link := range linksByFile {
		linkCopy := link
		tasks = append(tasks, func(taskCtx context.Context) error {
			return tree.putEntity(taskCtx, fileThumbnailEntityKind, fileThumbnailEntityID(linkCopy.FileID), linkCopy, true)
		})
	}
	entityCount := len(snapshot.Shares) + len(snapshot.DAVLocks) + len(snapshot.Uploads) + len(snapshot.Thumbnails) + len(linksByFile)
	if entityCount > 0 {
		entityTasks := tasks[len(tasks)-entityCount:]
		if e := runTreeImportTasks(ctx, 32, entityTasks); e != nil {
			return TreeValidation{}, e
		}
	}
	for h, size := range physical {
		if h != "" {
			st.PhysicalObjects++
			st.PhysicalBytes += size
		}
	}
	directories := make([]string, 0, len(byDir))
	for dir := range byDir {
		directories = append(directories, dir)
	}
	sort.Slice(directories, func(i, j int) bool {
		leftDepth := strings.Count(strings.Trim(directories[i], "/"), "/")
		rightDepth := strings.Count(strings.Trim(directories[j], "/"), "/")
		if leftDepth == rightDepth {
			return directories[i] > directories[j]
		}
		return leftDepth > rightDepth
	})
	// Deepest-first makes every directory entry hydrate from an already
	// finalized child manifest, producing exact aggregates in one pass.
	for _, dir := range directories {
		if e := tree.writeIndex(ctx, directoryIndex{Version: 2, AggregateVersion: directoryAggregateVersion, Directory: dir, Records: byDir[dir]}, 0, false); e != nil {
			return TreeValidation{}, fmt.Errorf("write directory index %q: %w", dir, e)
		}
	}
	for id, root := range trashRoots {
		m := trashManifest{Version: 2, ID: id, Root: root, Deleting: root.TrashDeleting, CreatedAt: *root.TrashedAt}
		b, _ := marshalTree(m)
		z := int64(0)
		if _, e := tree.objects.Put(ctx, tree.trashManifestKey(id), b, &z); e != nil {
			return TreeValidation{}, e
		}
	}
	b, _ := marshalTree(st)
	if _, e := tree.objects.Put(ctx, tree.statsKey(), b, nil); e != nil {
		return TreeValidation{}, e
	}
	next := snapshot.NextFileID
	if next <= maxID {
		next = maxID + 1
	}
	b, _ = marshalTree(fileSequence{Next: next})
	if _, e := tree.objects.Put(ctx, tree.sequenceKey(), b, nil); e != nil {
		return TreeValidation{}, e
	}
	im := treeImportManifest{Version: 1, SourceSHA256: snapshot.SourceSHA256, SourceGeneration: snapshot.SourceGeneration, Records: len(snapshot.Records), Shares: len(snapshot.Shares), DAVLocks: len(snapshot.DAVLocks), Uploads: len(snapshot.Uploads), Thumbnails: len(snapshot.Thumbnails), Active: validation.Active, Trash: validation.Trash, Files: validation.Files, Directories: validation.Directories, Bytes: validation.Bytes, ImportedAt: time.Now().UTC()}
	b, _ = marshalTree(im)
	z := int64(0)
	if _, e := tree.objects.Put(ctx, tree.prefix+"/migration/import.json", b, &z); e != nil {
		return TreeValidation{}, e
	}
	activeKeys, e := tree.objects.List(ctx, tree.prefix+"/tree/nodes/")
	if e != nil {
		return TreeValidation{}, e
	}
	trashKeys, e := tree.objects.List(ctx, tree.prefix+"/trash/")
	if e != nil {
		return TreeValidation{}, e
	}
	actualTrash := 0
	for _, key := range trashKeys {
		if !strings.HasSuffix(key, "/manifest.json") {
			actualTrash++
		}
	}
	if len(activeKeys) != validation.Active || actualTrash != validation.Trash {
		return TreeValidation{}, fmt.Errorf("bulk import verification mismatch: active %d/%d trash %d/%d", len(activeKeys), validation.Active, actualTrash, validation.Trash)
	}
	indexedRecords := 0
	for dir, expectedRecords := range byDir {
		idx, _, exists, indexErr := tree.getIndexManifest(ctx, dir)
		if indexErr != nil {
			return TreeValidation{}, indexErr
		}
		if !exists || idx.Total != len(expectedRecords) {
			return TreeValidation{}, fmt.Errorf("bulk import index mismatch for %s: total %d/%d", dir, idx.Total, len(expectedRecords))
		}
		pageRecords := 0
		for _, page := range idx.Pages {
			pageRecords += page.Count
		}
		if pageRecords != idx.Total {
			return TreeValidation{}, fmt.Errorf("bulk import index page mismatch for %s: pages %d/%d", dir, pageRecords, idx.Total)
		}
		indexedRecords += idx.Total
	}
	if indexedRecords != validation.Active {
		return TreeValidation{}, fmt.Errorf("bulk import indexed record mismatch: got %d want %d", indexedRecords, validation.Active)
	}
	return validation, nil
}
func runTreeImportTasks(ctx context.Context, workers int, tasks []func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan func(context.Context) error)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if e := task(ctx); e != nil {
					select {
					case errs <- e:
						cancel()
					default:
						{
						}
					}
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, task := range tasks {
			select {
			case jobs <- task:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()
	select {
	case e := <-errs:
		return e
	default:
		return ctx.Err()
	}
}
func ImportTreeRecords(ctx context.Context, store Store, records []FileRecord) error {
	_, e := BulkImportTree(ctx, store, TreeImportSnapshot{Records: records})
	return e
}
func ValidateTreeRecords(ctx context.Context, store Store, expected int) (TreeValidation, error) {
	all, e := store.ListAll(ctx)
	if e != nil {
		return TreeValidation{}, e
	}
	trash, e := store.ListTrashRecords(ctx, nil)
	if e != nil {
		return TreeValidation{}, e
	}
	v := TreeValidation{Expected: expected, Actual: len(all) + len(trash), Active: len(all), Trash: len(trash)}
	for _, r := range append(all, trash...) {
		if r.IsDirectory {
			v.Directories++
		} else {
			v.Files++
			v.Bytes += r.Size
		}
	}
	if v.Actual != expected {
		return v, fmt.Errorf("tree record count mismatch: got %d want %d", v.Actual, expected)
	}
	return v, nil
}
