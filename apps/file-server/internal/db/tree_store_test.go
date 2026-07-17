package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestTree(t *testing.T) *TreeStore {
	t.Helper()
	store, e := NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if e != nil {
		t.Fatal(e)
	}
	tree := store.(*TreeStore)
	t.Cleanup(tree.Close)
	if e = tree.EnsureSchema(context.Background()); e != nil {
		t.Fatal(e)
	}
	return tree
}

func TestTrashRestoreResumeAfterStatsBeforeCompletionIsExact(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	if e := s.UpsertDirectory(ctx, "/src"); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "/src/a.txt", "h", 7); e != nil {
		t.Fatal(e)
	}
	trashID := "trash-resume"
	op, e := s.CreateTrashOperation(ctx, []TrashPath{{Path: "/src", TrashID: trashID}})
	if e != nil {
		t.Fatal(e)
	}
	stored, g, _, _ := s.loadOperation(ctx, op.ID)
	root, _, _ := s.Find(ctx, "/src")
	at := time.Now().UTC()
	root.TrashedAt = &at
	root.TrashID = trashID
	root.TrashRoot = true
	stored.Total = 2
	stored.Result = []FileRecord{root}
	stored.StatsDelta = MetadataStats{LogicalFiles: -1, LogicalDirs: -1, LogicalBytes: -7, PhysicalObjects: -1, PhysicalBytes: -7}
	if _, e = s.saveOperationCAS(ctx, stored, g); e != nil {
		t.Fatal(e)
	}
	if _, e = s.trashPathsInternal(ctx, stored.TrashItems, nil, false); e != nil {
		t.Fatal(e)
	}
	// Crash window: root marker is gone while its parent/index records remain.
	staleRoot := root
	staleRoot.TrashedAt = nil
	staleRoot.TrashID = ""
	staleRoot.TrashRoot = false
	if e = s.updateIndexRecord(ctx, "/", staleRoot, false); e != nil {
		t.Fatal(e)
	}
	if e = s.writeIndex(ctx, directoryIndex{Version: 1, Directory: "/src"}, 0, false); e != nil {
		t.Fatal(e)
	}
	if e = s.mutateStatsOnce(ctx, stored.ID, stored.StatsDelta); e != nil {
		t.Fatal(e)
	}
	done, e := s.RunOperation(ctx, stored.ID)
	if e != nil {
		t.Fatal(e)
	}
	if done.Status != "completed" || len(done.Result) != 1 {
		t.Fatalf("trash=%+v", done)
	}
	if _, _, exists, indexErr := s.getIndexManifest(ctx, "/src"); indexErr != nil || exists {
		t.Fatalf("orphan trash index exists=%v err=%v", exists, indexErr)
	}
	stats, _ := s.MetadataStats(ctx)
	if stats.LogicalFiles != 0 || stats.LogicalDirs != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	activePage, pageErr := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if pageErr != nil || activePage.FolderSummary != (FolderSummary{}) {
		t.Fatalf("trash aggregate=%+v err=%v", activePage.FolderSummary, pageErr)
	}
	restore, e := s.CreateRestoreOperation(ctx, []string{trashID})
	if e != nil {
		t.Fatal(e)
	}
	records, _ := s.ListTrashRecords(ctx, []string{trashID})
	stored, g, _, _ = s.loadOperation(ctx, restore.ID)
	stored.Total = len(records)
	stored.Result = []FileRecord{root}
	stored.StatsDelta = MetadataStats{LogicalFiles: 1, LogicalDirs: 1, LogicalBytes: 7, PhysicalObjects: 1, PhysicalBytes: 7}
	if _, e = s.saveOperationCAS(ctx, stored, g); e != nil {
		t.Fatal(e)
	}
	if _, e = s.restoreTrashInternal(ctx, stored.TrashIDs, nil, false); e != nil {
		t.Fatal(e)
	}
	if e = s.mutateStatsOnce(ctx, stored.ID, stored.StatsDelta); e != nil {
		t.Fatal(e)
	}
	done, e = s.RunOperation(ctx, stored.ID)
	if e != nil {
		t.Fatal(e)
	}
	stats, _ = s.MetadataStats(ctx)
	if stats.LogicalFiles != 1 || stats.LogicalDirs != 1 || len(done.Result) != 1 {
		t.Fatalf("restore=%+v stats=%+v", done, stats)
	}
	activePage, pageErr = s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if pageErr != nil || activePage.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 7}) {
		t.Fatalf("restore aggregate=%+v err=%v", activePage.FolderSummary, pageErr)
	}
}

func TestMoveResumeCleansSourceIndexAfterRootMarkerDeleted(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, d := range []string{"/src", "/dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.CreateMoveOperation(ctx, []string{"/src"}, "/dest")
	if e != nil {
		t.Fatal(e)
	}
	old, _, _ := s.Find(ctx, "/src")
	target := old
	target.LogicPath = "/dest/src"
	if e = s.putNode(ctx, target, true); e != nil {
		t.Fatal(e)
	}
	if e = s.updateIndexRecord(ctx, "/dest", target, false); e != nil {
		t.Fatal(e)
	}
	if e = s.deleteNode(ctx, old); e != nil {
		t.Fatal(e)
	}
	done, e := s.RunOperation(ctx, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if done.Status != "completed" {
		t.Fatalf("op=%+v", done)
	}
	rootPage, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range rootPage.Records {
		if r.LogicPath == "/src" {
			t.Fatal("stale source parent index")
		}
	}
	if _, _, exists, e := s.getIndexManifest(ctx, "/src"); e != nil || exists {
		t.Fatalf("orphan source index exists=%v err=%v", exists, e)
	}
}

func TestMoveResumeAfterTargetIndexBeforeSourceDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, d := range []string{"/src", "/dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "/src/a.txt", "h", 1); e != nil {
		t.Fatal(e)
	}
	op, e := s.CreateMoveOperation(ctx, []string{"/src"}, "/dest")
	if e != nil {
		t.Fatal(e)
	}
	old, _, _ := s.Find(ctx, "/src/a.txt")
	target := old
	target.LogicPath = "/dest/src/a.txt"
	if e = s.putNode(ctx, target, true); e != nil {
		t.Fatal(e)
	}
	if e = s.updateIndexRecord(ctx, "/dest/src", target, false); e != nil {
		t.Fatal(e)
	}
	done, e := s.RunOperation(ctx, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if done.Status != "completed" {
		t.Fatalf("op=%+v", done)
	}
	if _, ok, _ := s.Find(ctx, "/src/a.txt"); ok {
		t.Fatal("source remains")
	}
	if _, _, exists, indexErr := s.getIndexManifest(ctx, "/src"); indexErr != nil || exists {
		t.Fatalf("orphan move index exists=%v err=%v", exists, indexErr)
	}
	page, e := s.ListDirectChildren(ctx, "/dest/src", DirectChildrenOptions{Limit: 10})
	if e != nil || len(page.Records) != 1 {
		t.Fatalf("page=%+v err=%v", page, e)
	}
}

func TestTreePagedIndexReadsRequestedWindow(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	if e := s.UpsertDirectory(ctx, "/many"); e != nil {
		t.Fatal(e)
	}
	records := make([]FileRecord, 600)
	for i := range records {
		records[i] = FileRecord{ID: i + 10, LogicPath: fmt.Sprintf("/many/%04d.txt", i), PhysicalHash: fmt.Sprintf("h%d", i), Size: int64(i), UpdatedAt: time.Now().UTC()}
	}
	if _, e := BulkImportTree(ctx, newEmptyTree(t), TreeImportSnapshot{Records: records, NextFileID: 1000}); e != nil {
		t.Fatal(e)
	}
	// Exercise the runtime index built by normal writes as well.
	for i := 0; i < 520; i++ {
		if e := s.UpsertFile(ctx, fmt.Sprintf("/many/%04d.txt", i), fmt.Sprintf("h%d", i), int64(i)); e != nil {
			t.Fatal(e)
		}
	}
	p, e := s.ListDirectChildren(ctx, "/many", DirectChildrenOptions{Offset: 300, Limit: 25})
	if e != nil {
		t.Fatal(e)
	}
	if p.Total != 520 || len(p.Records) != 25 || p.Records[0].LogicPath != "/many/0300.txt" {
		t.Fatalf("page=%+v first=%v", p, p.Records[0])
	}
	if p.FolderSummary != (FolderSummary{Files: 520, Bytes: 134940}) {
		t.Fatalf("summary=%+v", p.FolderSummary)
	}
	idx, _, ok, e := s.getIndexManifest(ctx, "/many")
	if e != nil || !ok || len(idx.Pages) < 2 {
		t.Fatalf("index=%+v ok=%v err=%v", idx, ok, e)
	}
	var pageFiles, pageBytes int64
	for _, descriptor := range idx.Pages {
		pageFiles += descriptor.SubtreeFiles
		pageBytes += descriptor.SubtreeBytes
	}
	if pageFiles != 520 || pageBytes != 134940 || idx.AggregateVersion != directoryAggregateVersion {
		t.Fatalf("page files=%d bytes=%d index=%+v", pageFiles, pageBytes, idx)
	}
}

func TestTreeFolderSummaryPropagatesAbsoluteValuesToRoot(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, dir := range []string{"/a", "/a/b"} {
		if e := s.UpsertDirectory(ctx, dir); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "/a/direct.bin", "direct", 3); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "/a/b/nested.bin", "nested", 10); e != nil {
		t.Fatal(e)
	}

	root, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	if root.FolderSummary != (FolderSummary{Files: 2, Directories: 2, Bytes: 13}) {
		t.Fatalf("root summary=%+v", root.FolderSummary)
	}
	if len(root.Records) != 1 || root.Records[0].FolderSummary == nil || *root.Records[0].FolderSummary != (FolderSummary{Files: 2, Directories: 1, Bytes: 13}) {
		t.Fatalf("root records=%+v", root.Records)
	}

	if e = s.UpsertFile(ctx, "/a/b/nested.bin", "nested-v2", 20); e != nil {
		t.Fatal(e)
	}
	root, e = s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary.Bytes != 23 || root.FolderSummary.Files != 2 {
		t.Fatalf("overwrite summary=%+v err=%v", root.FolderSummary, e)
	}
	if e = s.DeletePath(ctx, "/a/direct.bin"); e != nil {
		t.Fatal(e)
	}
	root, e = s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Files: 1, Directories: 2, Bytes: 20}) {
		t.Fatalf("delete summary=%+v err=%v", root.FolderSummary, e)
	}
}

func TestTreeFolderSummaryDoesNotChangeWithSearchOrPagination(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	if e := s.UpsertDirectory(ctx, "/folder"); e != nil {
		t.Fatal(e)
	}
	for name, size := range map[string]int64{"alpha.txt": 4, "beta.txt": 6} {
		if e := s.UpsertFile(ctx, "/folder/"+name, name, size); e != nil {
			t.Fatal(e)
		}
	}
	p, e := s.ListDirectChildren(ctx, "/folder", DirectChildrenOptions{Query: "alpha", Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if p.Total != 1 || p.TotalBytes != 4 || p.FolderSummary != (FolderSummary{Files: 2, Bytes: 10}) {
		t.Fatalf("search page=%+v", p)
	}
}

func TestTreeFolderSummarySerializesAcrossStoreInstances(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "shared-aggregates")
	aRaw, e := NewTreeLocal(root, "")
	if e != nil {
		t.Fatal(e)
	}
	bRaw, e := NewTreeLocal(root, "")
	if e != nil {
		t.Fatal(e)
	}
	a, b := aRaw.(*TreeStore), bRaw.(*TreeStore)
	defer a.Close()
	defer b.Close()
	if e = a.EnsureSchema(ctx); e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		errs <- a.UpsertFile(ctx, "/a.bin", "a", 1)
	}()
	go func() {
		<-start
		errs <- b.UpsertFile(ctx, "/b.bin", "b", 2)
	}()
	close(start)
	for range 2 {
		if e = <-errs; e != nil {
			t.Fatal(e)
		}
	}
	p, e := a.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || p.FolderSummary != (FolderSummary{Files: 2, Bytes: 3}) {
		t.Fatalf("summary=%+v err=%v", p.FolderSummary, e)
	}
}

func newEmptyTree(t *testing.T) Store {
	t.Helper()
	s, e := NewTreeLocal(filepath.Join(t.TempDir(), "bulk"), "")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}

func TestBulkImportPreservesUnicodeIDsTrashAndStats(t *testing.T) {
	ctx := context.Background()
	s := newEmptyTree(t)
	now := time.Now().UTC()
	long := strings.Repeat("日本語", 100) + ".zip"
	records := []FileRecord{{ID: 41, LogicPath: "/" + long, PhysicalHash: "object-a", Size: 99, UpdatedAt: now}, {ID: 42, LogicPath: "/deleted.txt", PhysicalHash: "object-b", Size: 5, UpdatedAt: now, TrashedAt: &now, TrashID: "trash-1", TrashRoot: true}}
	v, e := BulkImportTree(ctx, s, TreeImportSnapshot{Records: records, NextFileID: 80, SourceSHA256: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	if v.Active != 1 || v.Trash != 1 {
		t.Fatalf("validation=%+v", v)
	}
	r, ok, e := s.Find(ctx, "/"+long)
	if e != nil || !ok || r.ID != 41 {
		t.Fatalf("record=%+v ok=%v err=%v", r, ok, e)
	}
	st, e := s.(MetadataStatsProvider).MetadataStats(ctx)
	if e != nil || st.LogicalFiles != 1 || st.LogicalBytes != 99 {
		t.Fatalf("stats=%+v err=%v", st, e)
	}
	trash, e := s.ListTrash(ctx)
	if e != nil || len(trash) != 1 || trash[0].ID != 42 {
		t.Fatalf("trash=%+v err=%v", trash, e)
	}
}

func TestBulkImportBuildsAggregatesDeepestFirst(t *testing.T) {
	ctx := context.Background()
	s := newEmptyTree(t)
	now := time.Now().UTC()
	records := []FileRecord{
		{ID: 1, LogicPath: "/a", IsDirectory: true, UpdatedAt: now},
		{ID: 2, LogicPath: "/a/empty", IsDirectory: true, UpdatedAt: now},
		{ID: 3, LogicPath: "/a/nested", IsDirectory: true, UpdatedAt: now},
		{ID: 4, LogicPath: "/a/nested/file.bin", PhysicalHash: "h", Size: 42, UpdatedAt: now},
	}
	if _, e := BulkImportTree(ctx, s, TreeImportSnapshot{Records: records, NextFileID: 5}); e != nil {
		t.Fatal(e)
	}
	p, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || p.FolderSummary != (FolderSummary{Files: 1, Directories: 3, Bytes: 42}) {
		t.Fatalf("root summary=%+v err=%v", p.FolderSummary, e)
	}
	p, e = s.ListDirectChildren(ctx, "/a", DirectChildrenOptions{Limit: 10})
	if e != nil || p.FolderSummary != (FolderSummary{Files: 1, Directories: 2, Bytes: 42}) {
		t.Fatalf("/a summary=%+v err=%v", p.FolderSummary, e)
	}
	for _, r := range p.Records {
		if r.LogicPath == "/a/empty" && (r.FolderSummary == nil || *r.FolderSummary != (FolderSummary{})) {
			t.Fatalf("empty summary=%+v", r.FolderSummary)
		}
	}
}

func TestTreeRejectsMetadataObjectKeyOverGCSLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	parts := make([]string, 20)
	for i := range parts {
		parts[i] = strings.Repeat(fmt.Sprintf("x%d", i), 40)
	}
	path := "/" + strings.Join(parts, "/") + "/file.txt"
	if e := s.UpsertFile(ctx, path, "hash", 1); e == nil || !strings.Contains(e.Error(), "1024") {
		t.Fatalf("error=%v", e)
	}
}

func TestTreeStoresSerializeConflictingDAVLocks(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "shared")
	aRaw, e := NewTreeLocal(root, "")
	if e != nil {
		t.Fatal(e)
	}
	bRaw, e := NewTreeLocal(root, "")
	if e != nil {
		t.Fatal(e)
	}
	a, b := aRaw.(*TreeStore), bRaw.(*TreeStore)
	defer a.Close()
	defer b.Close()
	if e = a.EnsureSchema(ctx); e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, s := range []*TreeStore{a, b} {
		go func(i int, s *TreeStore) {
			<-start
			_, e := s.CreateDAVLock(ctx, DAVLockRecord{Token: fmt.Sprintf("t%d", i), Path: "/same", Depth: 0, ExpiresAt: time.Now().Add(time.Hour)})
			results <- e
		}(i, s)
	}
	close(start)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if e == ErrDAVLockConflict {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestMoveOperationResumesWithoutInflatingTotal(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, d := range []string{"/src", "/dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 20; i++ {
		if e := s.UpsertFile(ctx, fmt.Sprintf("/src/%02d.txt", i), fmt.Sprintf("h%d", i), 1); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.CreateMoveOperation(ctx, []string{"/src"}, "/dest")
	if e != nil {
		t.Fatal(e)
	}
	// Simulate a crashed worker after one durable node and checkpoint.
	old, ok, e := s.Find(ctx, "/src/00.txt")
	if e != nil || !ok {
		t.Fatal(e)
	}
	moved := old
	moved.LogicPath = "/dest/src/00.txt"
	if e = s.putNode(ctx, moved, true); e != nil {
		t.Fatal(e)
	}
	if e = s.deleteNode(ctx, old); e != nil {
		t.Fatal(e)
	}
	_ = s.updateIndexRecord(ctx, "/src", old, true)
	_ = s.updateIndexRecord(ctx, "/dest/src", moved, false)
	stored, g, _, e := s.loadOperation(ctx, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	expired := time.Now().Add(-time.Minute)
	stored.Status = "running"
	stored.Progress = 1
	stored.Total = 21
	stored.LeaseUntil = &expired
	if _, e = s.saveOperationCAS(ctx, stored, g); e != nil {
		t.Fatal(e)
	}
	done, e := s.RunOperation(ctx, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if done.Total != 21 || done.Progress != 21 || len(done.Result) != 1 {
		t.Fatalf("operation=%+v", done)
	}
	if _, ok, e = s.Find(ctx, "/dest/src/19.txt"); e != nil || !ok {
		t.Fatalf("moved=%v err=%v", ok, e)
	}
	page, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || page.FolderSummary != (FolderSummary{Files: 20, Directories: 2, Bytes: 20}) {
		t.Fatalf("resume aggregate=%+v err=%v", page.FolderSummary, e)
	}
}

func TestMoveOperationSupportsMultipleRootsWithSummaryOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, d := range []string{"/a", "/b", "/dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.CreateMoveOperation(ctx, []string{"/a", "/b"}, "/dest")
	if e != nil {
		t.Fatal(e)
	}
	done, e := s.RunOperation(ctx, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if done.Total != 2 || len(done.Result) != 2 {
		t.Fatalf("operation=%+v", done)
	}
	dest, e := s.ListDirectChildren(ctx, "/dest", DirectChildrenOptions{Limit: 10})
	if e != nil || dest.FolderSummary != (FolderSummary{Directories: 2}) {
		t.Fatalf("empty directory move aggregate=%+v err=%v", dest.FolderSummary, e)
	}
	root, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Directories: 3}) {
		t.Fatalf("root empty directory aggregate=%+v err=%v", root.FolderSummary, e)
	}
}

func TestMoveTrashRestoreOperationsKeepFolderAggregatesExact(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, dir := range []string{"/src", "/src/child", "/dest"} {
		if e := s.UpsertDirectory(ctx, dir); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "/src/child/data.bin", "data", 10); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "/dest/keep.bin", "keep", 2); e != nil {
		t.Fatal(e)
	}
	move, e := s.CreateMoveOperation(ctx, []string{"/src"}, "/dest")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOperation(ctx, move.ID); e != nil {
		t.Fatal(e)
	}
	dest, e := s.ListDirectChildren(ctx, "/dest", DirectChildrenOptions{Limit: 10})
	if e != nil || dest.FolderSummary != (FolderSummary{Files: 2, Directories: 2, Bytes: 12}) {
		t.Fatalf("move aggregate=%+v err=%v", dest.FolderSummary, e)
	}
	root, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Files: 2, Directories: 3, Bytes: 12}) {
		t.Fatalf("root after move=%+v err=%v", root.FolderSummary, e)
	}

	trashID := "aggregate-trash"
	trash, e := s.CreateTrashOperation(ctx, []TrashPath{{Path: "/dest/src", TrashID: trashID}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOperation(ctx, trash.ID); e != nil {
		t.Fatal(e)
	}
	manifest, ok, e := s.GetTrashManifest(ctx, trashID)
	if e != nil || !ok || manifest.Root.FolderSummary == nil || *manifest.Root.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 10}) {
		t.Fatalf("trash manifest=%+v ok=%v err=%v", manifest, ok, e)
	}
	root, e = s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 2}) {
		t.Fatalf("root after trash=%+v err=%v", root.FolderSummary, e)
	}

	restore, e := s.CreateRestoreOperation(ctx, []string{trashID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOperation(ctx, restore.ID); e != nil {
		t.Fatal(e)
	}
	root, e = s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Files: 2, Directories: 3, Bytes: 12}) {
		t.Fatalf("root after restore=%+v err=%v", root.FolderSummary, e)
	}
}

func TestHardDeleteTrashDoesNotChangeActiveFolderAggregate(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, dir := range []string{"/keep", "/gone"} {
		if e := s.UpsertDirectory(ctx, dir); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "/keep/a.bin", "a", 5); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "/gone/b.bin", "b", 10); e != nil {
		t.Fatal(e)
	}
	trashID := "hard-delete-aggregate"
	if _, e := s.TrashPaths(ctx, []TrashPath{{Path: "/gone", TrashID: trashID}}); e != nil {
		t.Fatal(e)
	}
	before, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimTrash(ctx, []string{trashID}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DeleteTrash(ctx, []string{trashID}); e != nil {
		t.Fatal(e)
	}
	after, e := s.ListDirectChildren(ctx, "/", DirectChildrenOptions{Limit: 10})
	if e != nil || after.FolderSummary != before.FolderSummary || after.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 5}) {
		t.Fatalf("before=%+v after=%+v err=%v", before.FolderSummary, after.FolderSummary, e)
	}
}
