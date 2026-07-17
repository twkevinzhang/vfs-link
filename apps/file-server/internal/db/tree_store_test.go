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
}
