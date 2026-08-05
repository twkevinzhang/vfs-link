package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTreeDriftActionsListPaginateAndDismiss(t *testing.T) {
	ctx := context.Background()
	store := newTestTree(t)
	base := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	for index, id := range []string{"old", "middle", "new"} {
		action := DriftActionRecord{
			ID: id, PlanID: "plan", IdempotencyKey: "key-" + id,
			Status: "completed", Checkpoint: "completed",
			CreatedAt: base.Add(time.Duration(index) * time.Minute),
			UpdatedAt: base.Add(time.Duration(index) * time.Minute),
		}
		action.Payload, _ = json.Marshal(action)
		if _, err := store.CreateDriftAction(ctx, action); err != nil {
			t.Fatal(err)
		}
	}

	page, err := store.ListDriftActions(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != "middle" {
		t.Fatalf("page = %+v, want middle", page)
	}
	all, err := store.ListDriftActions(ctx, 0, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %+v, error %v", all, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		found, err := store.DismissDriftAction(ctx, "middle", base.Add(time.Hour))
		if err != nil || !found {
			t.Fatalf("DismissDriftAction attempt %d = %t, %v", attempt+1, found, err)
		}
	}
	if found, err := store.DismissDriftAction(ctx, "missing", base); err != nil || found {
		t.Fatalf("dismiss missing = %t, %v", found, err)
	}

	visible, err := store.ListDriftActions(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 || visible[0].ID != "new" || visible[1].ID != "old" {
		t.Fatalf("visible = %+v, want new and old", visible)
	}
	stored, ok, err := store.FindDriftAction(ctx, "middle")
	if err != nil || !ok {
		t.Fatalf("dismissed action lookup = %t, %v", ok, err)
	}
	stored.UpdatedAt = base.Add(2 * time.Hour)
	if _, err := store.UpdateDriftAction(ctx, stored, stored.Version); err != nil {
		t.Fatalf("update dismissed action: %v", err)
	}
	visible, err = store.ListDriftActions(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 {
		t.Fatalf("visible after dismissed action update = %+v, want dismissal to remain", visible)
	}
	if err := store.RestoreDriftAction(ctx, "middle"); err != nil {
		t.Fatal(err)
	}
	visible, err = store.ListDriftActions(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 3 || visible[0].ID != "new" || visible[1].ID != "middle" {
		t.Fatalf("visible after restore = %+v, want stable createdAt order with middle visible", visible)
	}
}

func TestTreeDriftScanIsSingletonAndUsesCAS(t *testing.T) {
	ctx := context.Background()
	store := newTestTree(t)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	newRecord := func(id string) DriftScanRecord {
		record := DriftScanRecord{ID: id, Status: "pending", Phase: "queued", CreatedAt: now, UpdatedAt: now}
		record.Payload, _ = json.Marshal(record)
		return record
	}

	first, created, err := store.StartDriftScan(ctx, newRecord("scan-one"))
	if err != nil || !created || first.ID != "scan-one" {
		t.Fatalf("first scan = %+v, created %t, error %v", first, created, err)
	}
	duplicate, created, err := store.StartDriftScan(ctx, newRecord("scan-duplicate"))
	if err != nil || created || duplicate.ID != first.ID {
		t.Fatalf("duplicate scan = %+v, created %t, error %v", duplicate, created, err)
	}

	first.Status, first.Phase, first.UpdatedAt = "completed", "completed", now.Add(time.Minute)
	first.Payload, _ = json.Marshal(first)
	completed, err := store.UpdateDriftScan(ctx, first, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDriftScan(ctx, first, first.Version); !errors.Is(err, ErrDriftStateConflict) {
		t.Fatalf("stale scan update error = %v, want conflict", err)
	}

	next, created, err := store.StartDriftScan(ctx, newRecord("scan-two"))
	if err != nil || !created || next.ID != "scan-two" || next.Version == completed.Version {
		t.Fatalf("next scan = %+v, created %t, error %v", next, created, err)
	}
	stored, found, err := store.FindDriftScan(ctx)
	if err != nil || !found || stored.ID != next.ID {
		t.Fatalf("stored scan = %+v, found %t, error %v", stored, found, err)
	}
}

type dismissRaceTreeBackend struct {
	treeBackend
	store   *TreeStore
	action  string
	raced   bool
	raceErr error
}

func (b *dismissRaceTreeBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	generation, err := b.treeBackend.Put(ctx, key, data, expected)
	if err != nil || b.raced || !strings.Contains(key, "/drift/action-dismissals/") {
		return generation, err
	}
	b.raced = true
	action, found, raceErr := b.store.FindDriftAction(ctx, b.action)
	if raceErr == nil && found {
		action.Status = "running"
		var payload map[string]any
		if json.Unmarshal(action.Payload, &payload) == nil {
			payload["status"] = "running"
			action.Payload, _ = json.Marshal(payload)
		}
		_, raceErr = b.store.UpdateDriftAction(ctx, action, action.Version)
	}
	b.raceErr = raceErr
	return generation, err
}

func TestTreeDismissRemovesTombstoneWhenActionStartsRunningConcurrently(t *testing.T) {
	base, err := newLocalTreeBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &dismissRaceTreeBackend{treeBackend: base, action: "race"}
	store := newTreeStore(backend, "race-dismiss")
	backend.store = store
	now := time.Now().UTC()
	record := DriftActionRecord{ID: "race", PlanID: "plan", IdempotencyKey: "race", Status: "failed", Checkpoint: "pending", CreatedAt: now, UpdatedAt: now}
	record.Payload, _ = json.Marshal(record)
	if _, err := store.CreateDriftAction(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	found, err := store.DismissDriftAction(context.Background(), record.ID, now)
	if !found || !errors.Is(err, ErrDriftActionNotTerminal) {
		t.Fatalf("DismissDriftAction = found %t, error %v", found, err)
	}
	if backend.raceErr != nil {
		t.Fatalf("race hook: %v", backend.raceErr)
	}
	if _, markerFound, err := backend.Get(context.Background(), store.driftActionDismissalKey(record.ID)); err != nil || markerFound {
		t.Fatalf("dismissal marker remains = %t, error %v", markerFound, err)
	}
}

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

func TestUploadRoundTripPersistsOpaqueResumableSessionURL(t *testing.T) {
	ctx := context.Background()
	store := newTestTree(t)
	want := UploadRecord{
		ID: "upload-session", LogicPath: "docs/report.txt", PhysicalHash: "docs/report.txt",
		Driver: "gcs", UploadURL: "https://storage.example/session/opaque-token",
		Status: "pending", ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	if _, err := store.CreateUpload(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.FindUpload(ctx, want.ID)
	if err != nil || !found {
		t.Fatalf("FindUpload() = found %t, error %v", found, err)
	}
	if got.UploadURL != want.UploadURL {
		t.Fatalf("UploadURL = %q, want opaque session URL", got.UploadURL)
	}
}

func TestCanonicalTreeIndexPathOnlyAliasesOuterWhitespace(t *testing.T) {
	if left, right := canonicalTreeIndexPath("Game/PCGame/archive"), canonicalTreeIndexPath("Game/PCGame/archive "); left != right {
		t.Fatalf("canonical directory paths differ: %q != %q", left, right)
	}
	if got := normalizeTreeRecord(FileRecord{LogicPath: "Game/PCGame/archive /file.bin"}).LogicPath; got != "Game/PCGame/archive /file.bin" {
		t.Fatalf("internal segment whitespace was changed: %q", got)
	}
	if left, right := encodeTreePath("Game/PCGame/archive"), encodeTreePath("Game/PCGame/archive "); left != right {
		t.Fatalf("canonical index keys differ: %q != %q", left, right)
	}
}

func TestBulkImportTreeMergesDirectoryIndexWhitespaceAliases(t *testing.T) {
	ctx := context.Background()
	target := newTestTree(t)
	snapshot := TreeImportSnapshot{
		Records: []FileRecord{
			{ID: 1, LogicPath: "archive", IsDirectory: true},
			{ID: 2, LogicPath: "archive /one.bin", PhysicalHash: "one", Size: 3},
			{ID: 3, LogicPath: "archive /two.bin", PhysicalHash: "two", Size: 5},
		},
		NextFileID: 4,
	}
	if _, err := BulkImportTree(ctx, target, snapshot); err != nil {
		t.Fatal(err)
	}
	page, err := target.ListDirectChildren(ctx, "archive", DirectChildrenOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 2 || page.FolderSummary != (FolderSummary{Files: 2, Bytes: 8}) {
		t.Fatalf("page=%+v", page)
	}
}

func TestTrashRestoreResumeAfterStatsBeforeCompletionIsExact(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	if e := s.UpsertDirectory(ctx, "src"); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "src/a.txt", "h", 7); e != nil {
		t.Fatal(e)
	}
	trashID := "trash-resume"
	op, e := s.CreateTrashOperation(ctx, []TrashPath{{Path: "src", TrashID: trashID}})
	if e != nil {
		t.Fatal(e)
	}
	stored, g, _, _ := s.loadOperation(ctx, op.ID)
	root, _, _ := s.Find(ctx, "src")
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
	if e = s.updateIndexRecord(ctx, "", staleRoot, false); e != nil {
		t.Fatal(e)
	}
	if e = s.writeIndex(ctx, directoryIndex{Version: 1, Directory: "src"}, 0, false); e != nil {
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
	if _, _, exists, indexErr := s.getIndexManifest(ctx, "src"); indexErr != nil || exists {
		t.Fatalf("orphan trash index exists=%v err=%v", exists, indexErr)
	}
	stats, _ := s.MetadataStats(ctx)
	if stats.LogicalFiles != 0 || stats.LogicalDirs != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	activePage, pageErr := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
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
	activePage, pageErr = s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if pageErr != nil || activePage.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 7}) {
		t.Fatalf("restore aggregate=%+v err=%v", activePage.FolderSummary, pageErr)
	}
}

func TestMoveResumeCleansSourceIndexAfterRootMarkerDeleted(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, d := range []string{"src", "dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.CreateMoveOperation(ctx, []string{"src"}, "dest")
	if e != nil {
		t.Fatal(e)
	}
	old, _, _ := s.Find(ctx, "src")
	target := old
	target.LogicPath = "dest/src"
	if e = s.putNode(ctx, target, true); e != nil {
		t.Fatal(e)
	}
	if e = s.updateIndexRecord(ctx, "dest", target, false); e != nil {
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
	rootPage, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range rootPage.Records {
		if r.LogicPath == "src" {
			t.Fatal("stale source parent index")
		}
	}
	if _, _, exists, e := s.getIndexManifest(ctx, "src"); e != nil || exists {
		t.Fatalf("orphan source index exists=%v err=%v", exists, e)
	}
}

func TestMoveResumeAfterTargetIndexBeforeSourceDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, d := range []string{"src", "dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "src/a.txt", "h", 1); e != nil {
		t.Fatal(e)
	}
	op, e := s.CreateMoveOperation(ctx, []string{"src"}, "dest")
	if e != nil {
		t.Fatal(e)
	}
	old, _, _ := s.Find(ctx, "src/a.txt")
	target := old
	target.LogicPath = "dest/src/a.txt"
	if e = s.putNode(ctx, target, true); e != nil {
		t.Fatal(e)
	}
	if e = s.updateIndexRecord(ctx, "dest/src", target, false); e != nil {
		t.Fatal(e)
	}
	done, e := s.RunOperation(ctx, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if done.Status != "completed" {
		t.Fatalf("op=%+v", done)
	}
	if _, ok, _ := s.Find(ctx, "src/a.txt"); ok {
		t.Fatal("source remains")
	}
	if _, _, exists, indexErr := s.getIndexManifest(ctx, "src"); indexErr != nil || exists {
		t.Fatalf("orphan move index exists=%v err=%v", exists, indexErr)
	}
	page, e := s.ListDirectChildren(ctx, "dest/src", DirectChildrenOptions{Limit: 10})
	if e != nil || len(page.Records) != 1 {
		t.Fatalf("page=%+v err=%v", page, e)
	}
}

func TestTreePagedIndexReadsRequestedWindow(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	if e := s.UpsertDirectory(ctx, "many"); e != nil {
		t.Fatal(e)
	}
	records := make([]FileRecord, 600)
	for i := range records {
		records[i] = FileRecord{ID: i + 10, LogicPath: fmt.Sprintf("many/%04d.txt", i), PhysicalHash: fmt.Sprintf("h%d", i), Size: int64(i), UpdatedAt: time.Now().UTC()}
	}
	if _, e := BulkImportTree(ctx, newEmptyTree(t), TreeImportSnapshot{Records: records, NextFileID: 1000}); e != nil {
		t.Fatal(e)
	}
	// Exercise the runtime index built by normal writes as well.
	for i := 0; i < 520; i++ {
		if e := s.UpsertFile(ctx, fmt.Sprintf("many/%04d.txt", i), fmt.Sprintf("h%d", i), int64(i)); e != nil {
			t.Fatal(e)
		}
	}
	p, e := s.ListDirectChildren(ctx, "many", DirectChildrenOptions{Offset: 300, Limit: 25})
	if e != nil {
		t.Fatal(e)
	}
	if p.Total != 520 || len(p.Records) != 25 || p.Records[0].LogicPath != "many/0300.txt" {
		t.Fatalf("page=%+v first=%v", p, p.Records[0])
	}
	if p.FolderSummary != (FolderSummary{Files: 520, Bytes: 134940}) {
		t.Fatalf("summary=%+v", p.FolderSummary)
	}
	idx, _, ok, e := s.getIndexManifest(ctx, "many")
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
	for _, dir := range []string{"a", "a/b"} {
		if e := s.UpsertDirectory(ctx, dir); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "a/direct.bin", "direct", 3); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "a/b/nested.bin", "nested", 10); e != nil {
		t.Fatal(e)
	}

	root, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	if root.FolderSummary != (FolderSummary{Files: 2, Directories: 2, Bytes: 13}) {
		t.Fatalf("root summary=%+v", root.FolderSummary)
	}
	if len(root.Records) != 1 || root.Records[0].FolderSummary == nil || *root.Records[0].FolderSummary != (FolderSummary{Files: 2, Directories: 1, Bytes: 13}) {
		t.Fatalf("root records=%+v", root.Records)
	}

	if e = s.UpsertFile(ctx, "a/b/nested.bin", "nested-v2", 20); e != nil {
		t.Fatal(e)
	}
	root, e = s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary.Bytes != 23 || root.FolderSummary.Files != 2 {
		t.Fatalf("overwrite summary=%+v err=%v", root.FolderSummary, e)
	}
	if e = s.DeletePath(ctx, "a/direct.bin"); e != nil {
		t.Fatal(e)
	}
	root, e = s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Files: 1, Directories: 2, Bytes: 20}) {
		t.Fatalf("delete summary=%+v err=%v", root.FolderSummary, e)
	}
}

func TestTreeFolderSummaryDoesNotChangeWithSearchOrPagination(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	if e := s.UpsertDirectory(ctx, "folder"); e != nil {
		t.Fatal(e)
	}
	for name, size := range map[string]int64{"alpha.txt": 4, "beta.txt": 6} {
		if e := s.UpsertFile(ctx, "folder/"+name, name, size); e != nil {
			t.Fatal(e)
		}
	}
	p, e := s.ListDirectChildren(ctx, "folder", DirectChildrenOptions{Query: "alpha", Limit: 1})
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
		errs <- a.UpsertFile(ctx, "a.bin", "a", 1)
	}()
	go func() {
		<-start
		errs <- b.UpsertFile(ctx, "b.bin", "b", 2)
	}()
	close(start)
	for range 2 {
		if e = <-errs; e != nil {
			t.Fatal(e)
		}
	}
	p, e := a.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
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
	records := []FileRecord{{ID: 41, LogicPath: "" + long, PhysicalHash: "object-a", Size: 99, UpdatedAt: now}, {ID: 42, LogicPath: "deleted.txt", PhysicalHash: "object-b", Size: 5, UpdatedAt: now, TrashedAt: &now, TrashID: "trash-1", TrashRoot: true}}
	v, e := BulkImportTree(ctx, s, TreeImportSnapshot{Records: records, NextFileID: 80, SourceSHA256: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	if v.Active != 1 || v.Trash != 1 {
		t.Fatalf("validation=%+v", v)
	}
	r, ok, e := s.Find(ctx, ""+long)
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
		{ID: 1, LogicPath: "a", IsDirectory: true, UpdatedAt: now},
		{ID: 2, LogicPath: "a/empty", IsDirectory: true, UpdatedAt: now},
		{ID: 3, LogicPath: "a/nested", IsDirectory: true, UpdatedAt: now},
		{ID: 4, LogicPath: "a/nested/file.bin", PhysicalHash: "h", Size: 42, UpdatedAt: now},
	}
	if _, e := BulkImportTree(ctx, s, TreeImportSnapshot{Records: records, NextFileID: 5}); e != nil {
		t.Fatal(e)
	}
	p, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || p.FolderSummary != (FolderSummary{Files: 1, Directories: 3, Bytes: 42}) {
		t.Fatalf("root summary=%+v err=%v", p.FolderSummary, e)
	}
	p, e = s.ListDirectChildren(ctx, "a", DirectChildrenOptions{Limit: 10})
	if e != nil || p.FolderSummary != (FolderSummary{Files: 1, Directories: 2, Bytes: 42}) {
		t.Fatalf("a summary=%+v err=%v", p.FolderSummary, e)
	}
	for _, r := range p.Records {
		if r.LogicPath == "a/empty" && (r.FolderSummary == nil || *r.FolderSummary != (FolderSummary{})) {
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
	path := strings.Join(parts, "/") + "/file.txt"
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
			_, e := s.CreateDAVLock(ctx, DAVLockRecord{Token: fmt.Sprintf("t%d", i), Path: "same", Depth: 0, ExpiresAt: time.Now().Add(time.Hour)})
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
	for _, d := range []string{"src", "dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 20; i++ {
		if e := s.UpsertFile(ctx, fmt.Sprintf("src/%02d.txt", i), fmt.Sprintf("h%d", i), 1); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.CreateMoveOperation(ctx, []string{"src"}, "dest")
	if e != nil {
		t.Fatal(e)
	}
	// Simulate a crashed worker after one durable node and checkpoint.
	old, ok, e := s.Find(ctx, "src/00.txt")
	if e != nil || !ok {
		t.Fatal(e)
	}
	moved := old
	moved.LogicPath = "dest/src/00.txt"
	if e = s.putNode(ctx, moved, true); e != nil {
		t.Fatal(e)
	}
	if e = s.deleteNode(ctx, old); e != nil {
		t.Fatal(e)
	}
	_ = s.updateIndexRecord(ctx, "src", old, true)
	_ = s.updateIndexRecord(ctx, "dest/src", moved, false)
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
	if _, ok, e = s.Find(ctx, "dest/src/19.txt"); e != nil || !ok {
		t.Fatalf("moved=%v err=%v", ok, e)
	}
	page, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || page.FolderSummary != (FolderSummary{Files: 20, Directories: 2, Bytes: 20}) {
		t.Fatalf("resume aggregate=%+v err=%v", page.FolderSummary, e)
	}
}

func TestRenameOperationResumesWithoutInflatingTotal(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	if e := s.UpsertDirectory(ctx, "src"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 20; i++ {
		if e := s.UpsertFile(ctx, fmt.Sprintf("src/%02d.txt", i), fmt.Sprintf("h%d", i), 1); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.CreateRenameOperation(ctx, "src", "renamed")
	if e != nil {
		t.Fatal(e)
	}
	if op.Type != "rename" || op.Destination != "renamed" {
		t.Fatalf("operation=%+v", op)
	}

	// Simulate a crash after a target node and its index were persisted, but
	// before the source node was removed from the operation manifest.
	old, ok, e := s.Find(ctx, "src/00.txt")
	if e != nil || !ok {
		t.Fatal(e)
	}
	renamed := old
	renamed.LogicPath = "renamed/00.txt"
	if e = s.putNode(ctx, renamed, true); e != nil {
		t.Fatal(e)
	}
	if e = s.deleteNode(ctx, old); e != nil {
		t.Fatal(e)
	}
	_ = s.updateIndexRecord(ctx, "src", old, true)
	_ = s.updateIndexRecord(ctx, "renamed", renamed, false)
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
	if done.Status != "completed" || done.Total != 21 || done.Progress != 21 || len(done.Result) != 1 {
		t.Fatalf("operation=%+v", done)
	}
	if _, ok, e = s.Find(ctx, "renamed/19.txt"); e != nil || !ok {
		t.Fatalf("renamed=%v err=%v", ok, e)
	}
	if _, ok, e = s.Find(ctx, "src"); e != nil || ok {
		t.Fatalf("source remains=%v err=%v", ok, e)
	}
	page, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || page.FolderSummary != (FolderSummary{Files: 20, Directories: 1, Bytes: 20}) {
		t.Fatalf("resume aggregate=%+v err=%v", page.FolderSummary, e)
	}
}

func TestMoveOperationSupportsMultipleRootsWithSummaryOnly(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, d := range []string{"a", "b", "dest"} {
		if e := s.UpsertDirectory(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.CreateMoveOperation(ctx, []string{"a", "b"}, "dest")
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
	dest, e := s.ListDirectChildren(ctx, "dest", DirectChildrenOptions{Limit: 10})
	if e != nil || dest.FolderSummary != (FolderSummary{Directories: 2}) {
		t.Fatalf("empty directory move aggregate=%+v err=%v", dest.FolderSummary, e)
	}
	root, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Directories: 3}) {
		t.Fatalf("root empty directory aggregate=%+v err=%v", root.FolderSummary, e)
	}
}

func TestMoveTrashRestoreOperationsKeepFolderAggregatesExact(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, dir := range []string{"src", "src/child", "dest"} {
		if e := s.UpsertDirectory(ctx, dir); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "src/child/data.bin", "data", 10); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "dest/keep.bin", "keep", 2); e != nil {
		t.Fatal(e)
	}
	move, e := s.CreateMoveOperation(ctx, []string{"src"}, "dest")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOperation(ctx, move.ID); e != nil {
		t.Fatal(e)
	}
	dest, e := s.ListDirectChildren(ctx, "dest", DirectChildrenOptions{Limit: 10})
	if e != nil || dest.FolderSummary != (FolderSummary{Files: 2, Directories: 2, Bytes: 12}) {
		t.Fatalf("move aggregate=%+v err=%v", dest.FolderSummary, e)
	}
	root, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Files: 2, Directories: 3, Bytes: 12}) {
		t.Fatalf("root after move=%+v err=%v", root.FolderSummary, e)
	}

	trashID := "aggregate-trash"
	trash, e := s.CreateTrashOperation(ctx, []TrashPath{{Path: "dest/src", TrashID: trashID}})
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
	root, e = s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
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
	root, e = s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || root.FolderSummary != (FolderSummary{Files: 2, Directories: 3, Bytes: 12}) {
		t.Fatalf("root after restore=%+v err=%v", root.FolderSummary, e)
	}
}

func TestHardDeleteTrashDoesNotChangeActiveFolderAggregate(t *testing.T) {
	ctx := context.Background()
	s := newTestTree(t)
	for _, dir := range []string{"keep", "gone"} {
		if e := s.UpsertDirectory(ctx, dir); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.UpsertFile(ctx, "keep/a.bin", "a", 5); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertFile(ctx, "gone/b.bin", "b", 10); e != nil {
		t.Fatal(e)
	}
	trashID := "hard-delete-aggregate"
	if _, e := s.TrashPaths(ctx, []TrashPath{{Path: "gone", TrashID: trashID}}); e != nil {
		t.Fatal(e)
	}
	before, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimTrash(ctx, []string{trashID}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DeleteTrash(ctx, []string{trashID}); e != nil {
		t.Fatal(e)
	}
	after, e := s.ListDirectChildren(ctx, "", DirectChildrenOptions{Limit: 10})
	if e != nil || after.FolderSummary != before.FolderSummary || after.FolderSummary != (FolderSummary{Files: 1, Directories: 1, Bytes: 5}) {
		t.Fatalf("before=%+v after=%+v err=%v", before.FolderSummary, after.FolderSummary, e)
	}
}
