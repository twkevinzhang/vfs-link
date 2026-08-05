package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (n *treeV4Namespace) trashRecordKey(record FileRecord) string {
	return n.store.prefix + "/v4/trash/records/" + encodeTreeSegment(record.TrashID) + "/" + encodeTreeSegment(strconv.Itoa(record.ID)) + ".json"
}

// BulkImportTreeV4 imports a validated v3 snapshot into an empty v4 namespace.
// Active nodes retain their legacy integer IDs while gaining stable UUIDs.
func BulkImportTreeV4(ctx context.Context, store Store, snapshot TreeImportSnapshot) (TreeValidation, error) {
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return TreeValidation{}, fmt.Errorf("v4 tree store is required")
	}
	validation, err := ValidateTreeImport(tree.prefix, snapshot)
	if err != nil {
		return TreeValidation{}, err
	}
	// Import is a maintenance-only fast path. Requiring the complete prefix to
	// be empty lets every object be written independently with create-only CAS.
	if existing, listErr := tree.objects.List(ctx, tree.prefix+"/"); listErr != nil {
		return TreeValidation{}, listErr
	} else if len(existing) != 0 {
		return TreeValidation{}, fmt.Errorf("v4 import prefix is not empty: %s", existing[0])
	}
	seenIDs := map[int]string{}
	active := make([]FileRecord, 0, len(snapshot.Records))
	for _, record := range snapshot.Records {
		if record.ID <= 0 {
			return TreeValidation{}, fmt.Errorf("record %s has invalid legacy id %d", record.LogicPath, record.ID)
		}
		if previous, exists := seenIDs[record.ID]; exists {
			return TreeValidation{}, fmt.Errorf("duplicate legacy id %d for %s and %s", record.ID, previous, record.LogicPath)
		}
		seenIDs[record.ID] = record.LogicPath
		if record.TrashedAt == nil {
			active = append(active, record)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		left, right := strings.Count(active[i].LogicPath, "/"), strings.Count(active[j].LogicPath, "/")
		if left == right {
			return active[i].LogicPath < active[j].LogicPath
		}
		return left < right
	})
	namespace := tree.v4
	root := treeV4Root{Version: treeV4Version, NodeID: "root", ShardCount: namespace.shardCount}
	rootNode := treeV4Node{NodeID: root.NodeID, IsDirectory: true, UpdatedAt: time.Now().UTC()}
	nodesByPath := map[string]treeV4Node{"": rootNode}
	shards := map[string]treeV4DirectoryShard{}
	var stats MetadataStats
	physical := map[string]int64{}
	tasks := make([]func(context.Context) error, 0, len(snapshot.Records)*2)
	appendPlain := func(key string, value any) error {
		payload, marshalErr := marshalTree(value)
		if marshalErr != nil {
			return marshalErr
		}
		tasks = append(tasks, func(taskCtx context.Context) error { return putV4ImportObject(taskCtx, tree.objects, key, payload) })
		return nil
	}
	appendEnvelope := func(key string, value any) error {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		return appendPlain(key, treeV4Envelope{Current: raw})
	}
	if err = appendPlain(namespace.rootKey(), root); err != nil {
		return TreeValidation{}, err
	}
	if err = appendEnvelope(namespace.nodeKey(root.NodeID), rootNode); err != nil {
		return TreeValidation{}, err
	}
	for _, record := range active {
		parentPath, name := parentLogicPath(record.LogicPath), pathpkg.Base(record.LogicPath)
		parent, exists := nodesByPath[parentPath]
		if !exists || !parent.IsDirectory {
			return TreeValidation{}, fmt.Errorf("parent directory not found: %s", parentPath)
		}
		stableName := snapshot.SourceSHA256 + "\x00" + strconv.Itoa(record.ID) + "\x00" + record.LogicPath
		node := treeV4Node{
			NodeID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(stableName)).String(), LegacyID: record.ID,
			PhysicalHash: record.PhysicalHash, Size: record.Size, IsDirectory: record.IsDirectory, UpdatedAt: record.UpdatedAt,
		}
		if node.UpdatedAt.IsZero() {
			node.UpdatedAt = time.Now().UTC()
		}
		nodesByPath[record.LogicPath] = node
		shardID := namespace.shardFor(name)
		shardKey := namespace.shardKey(parent.NodeID, shardID)
		shard, shardExists := shards[shardKey]
		if !shardExists {
			shard = treeV4DirectoryShard{DirectoryID: parent.NodeID, Shard: shardID, Entries: map[string]treeV4DirectoryEntry{}}
		}
		if _, collision := shard.Entries[name]; collision {
			return TreeValidation{}, fmt.Errorf("duplicate directory entry: %s", record.LogicPath)
		}
		shard.Entries[name] = treeV4DirectoryEntry{Name: name, Node: node}
		shards[shardKey] = shard
		if err = appendEnvelope(namespace.nodeKey(node.NodeID), node); err != nil {
			return TreeValidation{}, err
		}
		if err = appendEnvelope(namespace.legacyIDKey(node.LegacyID), struct {
			NodeID string `json:"nodeId"`
		}{node.NodeID}); err != nil {
			return TreeValidation{}, err
		}
		if node.IsDirectory {
			stats.LogicalDirs++
		} else {
			stats.LogicalFiles++
			stats.LogicalBytes += node.Size
			physical[node.PhysicalHash] = node.Size
		}
	}
	trashGroups := map[string][]FileRecord{}
	for _, record := range snapshot.Records {
		if record.TrashedAt != nil {
			trashGroups[record.TrashID] = append(trashGroups[record.TrashID], record)
		}
	}
	for trashID, records := range trashGroups {
		sort.Slice(records, func(i, j int) bool {
			left, right := strings.Count(records[i].LogicPath, "/"), strings.Count(records[j].LogicPath, "/")
			if left == right {
				return records[i].LogicPath < records[j].LogicPath
			}
			return left < right
		})
		trashNodes := map[string]treeV4Node{}
		var rootRecord FileRecord
		var rootNode treeV4Node
		for _, record := range records {
			stableName := snapshot.SourceSHA256 + "\x00trash\x00" + strconv.Itoa(record.ID) + "\x00" + record.LogicPath
			node := treeV4Node{
				NodeID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(stableName)).String(), LegacyID: record.ID,
				PhysicalHash: record.PhysicalHash, Size: record.Size, IsDirectory: record.IsDirectory, UpdatedAt: record.UpdatedAt,
			}
			if node.UpdatedAt.IsZero() {
				node.UpdatedAt = time.Now().UTC()
			}
			trashNodes[record.LogicPath] = node
			if err = appendEnvelope(namespace.nodeKey(node.NodeID), node); err != nil {
				return TreeValidation{}, err
			}
			if err = appendEnvelope(namespace.legacyIDKey(node.LegacyID), struct {
				NodeID string `json:"nodeId"`
			}{node.NodeID}); err != nil {
				return TreeValidation{}, err
			}
			if record.TrashRoot {
				rootRecord, rootNode = record, node
			}
		}
		if rootNode.NodeID == "" {
			return TreeValidation{}, fmt.Errorf("trash %s has no root record", trashID)
		}
		for _, record := range records {
			if record.TrashRoot {
				continue
			}
			parent, exists := trashNodes[parentLogicPath(record.LogicPath)]
			if !exists || !parent.IsDirectory {
				return TreeValidation{}, fmt.Errorf("trash %s parent missing for %s", trashID, record.LogicPath)
			}
			name := pathpkg.Base(record.LogicPath)
			shardID := namespace.shardFor(name)
			key := namespace.shardKey(parent.NodeID, shardID)
			shard, exists := shards[key]
			if !exists {
				shard = treeV4DirectoryShard{DirectoryID: parent.NodeID, Shard: shardID, Entries: map[string]treeV4DirectoryEntry{}}
			}
			shard.Entries[name] = treeV4DirectoryEntry{Name: name, Node: trashNodes[record.LogicPath]}
			shards[key] = shard
		}
		manifest := treeV4TrashManifest{Version: treeV4Version, ID: trashID, Root: rootRecord, Node: rootNode, Deleting: rootRecord.TrashDeleting, CreatedAt: *rootRecord.TrashedAt}
		if err = appendEnvelope(namespace.trashManifestKeyV4(trashID), manifest); err != nil {
			return TreeValidation{}, err
		}
	}
	for hash, size := range physical {
		if hash != "" {
			stats.PhysicalObjects++
			stats.PhysicalBytes += size
		}
	}
	for key, shard := range shards {
		if err = appendEnvelope(key, shard); err != nil {
			return TreeValidation{}, err
		}
	}
	for _, record := range snapshot.Shares {
		if err = appendPlain(tree.entityKey("shares", record.ID), record); err != nil {
			return TreeValidation{}, err
		}
	}
	for _, record := range snapshot.DAVLocks {
		if err = appendPlain(tree.entityKey("dav-locks", record.Token), record); err != nil {
			return TreeValidation{}, err
		}
	}
	for _, record := range snapshot.Uploads {
		if err = appendPlain(tree.entityKey("uploads", record.ID), record); err != nil {
			return TreeValidation{}, err
		}
	}
	for _, record := range snapshot.Thumbnails {
		if err = appendPlain(tree.entityKey("thumbnails", record.ID), record); err != nil {
			return TreeValidation{}, err
		}
	}
	for _, link := range snapshot.ThumbnailLinks {
		if err = appendPlain(tree.entityKey(fileThumbnailEntityKind, fileThumbnailEntityID(link.FileID)), link); err != nil {
			return TreeValidation{}, err
		}
	}
	for _, operation := range snapshot.Operations {
		if err = appendPlain(tree.operationKey(operation.ID), operation); err != nil {
			return TreeValidation{}, err
		}
	}
	if err = runTreeImportTasks(ctx, 32, tasks); err != nil {
		return TreeValidation{}, err
	}
	directorySummaries := make(map[string]FolderSummary, len(nodesByPath))
	for _, record := range active {
		for ancestor := parentLogicPath(record.LogicPath); ; ancestor = parentLogicPath(ancestor) {
			node := nodesByPath[ancestor]
			summary := directorySummaries[node.NodeID]
			if record.IsDirectory {
				summary.Directories++
			} else {
				summary.Files++
				summary.Bytes += record.Size
			}
			directorySummaries[node.NodeID] = summary
			if ancestor == "" {
				break
			}
		}
	}
	if err = tree.SeedDerivedMetadata(ctx, stats, directorySummaries); err != nil {
		return TreeValidation{}, err
	}
	// The completion object is the cutover gate. Publish it only after every
	// namespace and side-entity class can be read back through production APIs.
	if err = validateTreeV4ImportReadback(ctx, tree, snapshot); err != nil {
		return TreeValidation{}, err
	}
	completion := struct {
		Version      int       `json:"version"`
		SourceSHA256 string    `json:"sourceSha256"`
		Records      int       `json:"records"`
		CompletedAt  time.Time `json:"completedAt"`
	}{treeV4Version, snapshot.SourceSHA256, len(snapshot.Records), time.Now().UTC()}
	payload, _ := marshalTree(completion)
	if err = putV4ImportObject(ctx, tree.objects, tree.prefix+"/v4/import/completed.json", payload); err != nil {
		return TreeValidation{}, err
	}
	return validation, nil
}

func validateTreeV4ImportReadback(ctx context.Context, tree *TreeStore, source TreeImportSnapshot) error {
	readback, err := ExportTreeV4(ctx, tree)
	if err != nil {
		return fmt.Errorf("v4 import read-back: %w", err)
	}
	source.Shares = append([]ShareRecord(nil), source.Shares...)
	source.DAVLocks = append([]DAVLockRecord(nil), source.DAVLocks...)
	source.Uploads = append([]UploadRecord(nil), source.Uploads...)
	source.Thumbnails = append([]ThumbnailRecord(nil), source.Thumbnails...)
	source.ThumbnailLinks = append([]FileThumbnailLink(nil), source.ThumbnailLinks...)
	source.Operations = append([]OperationRecord(nil), source.Operations...)
	sort.Slice(source.Shares, func(i, j int) bool { return source.Shares[i].ID < source.Shares[j].ID })
	sort.Slice(source.DAVLocks, func(i, j int) bool { return source.DAVLocks[i].Token < source.DAVLocks[j].Token })
	sort.Slice(source.Uploads, func(i, j int) bool { return source.Uploads[i].ID < source.Uploads[j].ID })
	sort.Slice(source.Thumbnails, func(i, j int) bool { return source.Thumbnails[i].ID < source.Thumbnails[j].ID })
	sort.Slice(source.ThumbnailLinks, func(i, j int) bool { return source.ThumbnailLinks[i].FileID < source.ThumbnailLinks[j].FileID })
	sort.Slice(source.Operations, func(i, j int) bool { return source.Operations[i].ID < source.Operations[j].ID })
	type recordIdentity struct {
		ID            int
		PhysicalHash  string
		Size          int64
		IsDirectory   bool
		TrashRoot     bool
		TrashDeleting bool
	}
	recordKey := func(record FileRecord) string {
		state := "active"
		if record.TrashedAt != nil {
			state = "trash:" + record.TrashID
		}
		return state + "\x00" + cleanLogicPath(record.LogicPath)
	}
	toRecords := func(records []FileRecord) (map[string]recordIdentity, error) {
		out := make(map[string]recordIdentity, len(records))
		for _, record := range records {
			key := recordKey(record)
			if _, exists := out[key]; exists {
				return nil, fmt.Errorf("duplicate record %q", key)
			}
			out[key] = recordIdentity{record.ID, record.PhysicalHash, record.Size, record.IsDirectory, record.TrashRoot, record.TrashDeleting}
		}
		return out, nil
	}
	wantRecords, err := toRecords(source.Records)
	if err != nil {
		return fmt.Errorf("v4 import source: %w", err)
	}
	gotRecords, err := toRecords(readback.Records)
	if err != nil {
		return fmt.Errorf("v4 import read-back: %w", err)
	}
	if len(wantRecords) != len(gotRecords) {
		return fmt.Errorf("v4 import read-back record count=%d, want %d", len(gotRecords), len(wantRecords))
	}
	for key, want := range wantRecords {
		if got, exists := gotRecords[key]; !exists || got != want {
			return fmt.Errorf("v4 import read-back record %q=%+v, want %+v", key, got, want)
		}
	}
	type entitySet struct {
		name string
		want any
		got  any
	}
	sets := []entitySet{
		{"shares", source.Shares, readback.Shares},
		{"dav locks", source.DAVLocks, readback.DAVLocks},
		{"uploads", source.Uploads, readback.Uploads},
		{"thumbnails", source.Thumbnails, readback.Thumbnails},
		{"thumbnail links", source.ThumbnailLinks, readback.ThumbnailLinks},
		{"operations", source.Operations, readback.Operations},
	}
	for _, set := range sets {
		canonical := func(value any) ([]string, error) {
			payload, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return nil, marshalErr
			}
			var items []json.RawMessage
			if string(payload) != "null" {
				if marshalErr = json.Unmarshal(payload, &items); marshalErr != nil {
					return nil, marshalErr
				}
			}
			out := make([]string, len(items))
			for index := range items {
				out[index] = string(items[index])
			}
			sort.Strings(out)
			return out, nil
		}
		wantItems, marshalErr := canonical(set.want)
		if marshalErr != nil {
			return marshalErr
		}
		gotItems, marshalErr := canonical(set.got)
		if marshalErr != nil {
			return marshalErr
		}
		if len(wantItems) != len(gotItems) {
			return fmt.Errorf("v4 import read-back %s count=%d, want %d", set.name, len(gotItems), len(wantItems))
		}
		for index := range wantItems {
			if wantItems[index] != gotItems[index] {
				return fmt.Errorf("v4 import read-back %s mismatch", set.name)
			}
		}
	}
	return nil
}

func putV4ImportObject(ctx context.Context, backend treeBackend, key string, payload []byte) error {
	zero := int64(0)
	_, err := backend.Put(ctx, key, payload, &zero)
	return err
}

func (n *treeV4Namespace) importActiveRecord(ctx context.Context, record FileRecord, sourceHash string) error {
	record.LogicPath = cleanLogicPath(record.LogicPath)
	if record.LogicPath == "" {
		return fmt.Errorf("root record cannot be imported")
	}
	parentPath, name := parentLogicPath(record.LogicPath), pathpkg.Base(record.LogicPath)
	parent, found, err := n.resolve(ctx, parentPath)
	if err != nil || !found || !parent.IsDirectory {
		return fmt.Errorf("parent directory not found: %s", parentPath)
	}
	stableName := sourceHash + "\x00" + strconv.Itoa(record.ID) + "\x00" + record.LogicPath
	nodeID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(stableName)).String()
	node := treeV4Node{
		NodeID: nodeID, LegacyID: record.ID, PhysicalHash: record.PhysicalHash,
		Size: record.Size, IsDirectory: record.IsDirectory, UpdatedAt: record.UpdatedAt,
	}
	if node.UpdatedAt.IsZero() {
		node.UpdatedAt = n.now()
	}
	shardID := n.shardFor(name)
	resource := fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, shardID)
	resources := []string{"path:" + record.LogicPath, resource, fmt.Sprintf("legacy-id:%d", record.ID)}
	_, _, err = n.transact(ctx, resources, nil, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
		shard, _, readErr := n.readShard(buildCtx, parent.NodeID, shardID)
		if readErr != nil {
			return nil, nil, readErr
		}
		if _, exists := shard.Entries[name]; exists {
			return nil, nil, ErrPathConflict
		}
		shard.Entries[name] = treeV4DirectoryEntry{Name: name, Node: node}
		return []treeV4Mutation{
			{key: n.nodeKey(nodeID), value: node, requireAbsent: true, fence: fences[resource]},
			{key: n.shardKey(parent.NodeID, shardID), value: shard, fence: fences[resource]},
			{key: n.legacyIDKey(record.ID), value: struct {
				NodeID string `json:"nodeId"`
			}{nodeID}, requireAbsent: true, fence: fences[fmt.Sprintf("legacy-id:%d", record.ID)]},
		}, nil, nil
	})
	return err
}

// ExportTreeV4 creates a deterministic v3-compatible snapshot for validation,
// backup, or reverse migration during a maintenance window.
func ExportTreeV4(ctx context.Context, store Store) (TreeImportSnapshot, error) {
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return TreeImportSnapshot{}, fmt.Errorf("v4 tree store is required")
	}
	var snapshot TreeImportSnapshot
	active, err := tree.ListAll(ctx)
	if err != nil {
		return snapshot, err
	}
	snapshot.Records = append(snapshot.Records, active...)
	trashRecords, err := tree.v4.listTrashRecords(ctx, nil)
	if err != nil {
		return snapshot, err
	}
	snapshot.Records = append(snapshot.Records, trashRecords...)
	shares, err := tree.listEntities(ctx, "shares", func() any { return &ShareRecord{} })
	if err != nil {
		return snapshot, err
	}
	for _, value := range shares {
		snapshot.Shares = append(snapshot.Shares, *value.(*ShareRecord))
	}
	if snapshot.DAVLocks, err = tree.allDAVLocks(ctx); err != nil {
		return snapshot, err
	}
	uploads, err := tree.listEntities(ctx, "uploads", func() any { return &UploadRecord{} })
	if err != nil {
		return snapshot, err
	}
	for _, value := range uploads {
		snapshot.Uploads = append(snapshot.Uploads, *value.(*UploadRecord))
	}
	if snapshot.Thumbnails, err = tree.thumbnailRecords(ctx); err != nil {
		return snapshot, err
	}
	if snapshot.Operations, err = tree.listOperations(ctx); err != nil {
		return snapshot, err
	}
	links, err := tree.listEntities(ctx, fileThumbnailEntityKind, func() any { return &FileThumbnailLink{} })
	if err != nil {
		return snapshot, err
	}
	for _, value := range links {
		snapshot.ThumbnailLinks = append(snapshot.ThumbnailLinks, *value.(*FileThumbnailLink))
	}
	sort.Slice(snapshot.Records, func(i, j int) bool {
		if snapshot.Records[i].TrashedAt == nil && snapshot.Records[j].TrashedAt != nil {
			return true
		}
		if snapshot.Records[i].TrashedAt != nil && snapshot.Records[j].TrashedAt == nil {
			return false
		}
		return snapshot.Records[i].LogicPath < snapshot.Records[j].LogicPath
	})
	for _, record := range snapshot.Records {
		if record.ID >= snapshot.NextFileID {
			snapshot.NextFileID = record.ID + 1
		}
	}
	sort.Slice(snapshot.Shares, func(i, j int) bool { return snapshot.Shares[i].ID < snapshot.Shares[j].ID })
	sort.Slice(snapshot.DAVLocks, func(i, j int) bool { return snapshot.DAVLocks[i].Token < snapshot.DAVLocks[j].Token })
	sort.Slice(snapshot.Uploads, func(i, j int) bool { return snapshot.Uploads[i].ID < snapshot.Uploads[j].ID })
	sort.Slice(snapshot.Thumbnails, func(i, j int) bool { return snapshot.Thumbnails[i].ID < snapshot.Thumbnails[j].ID })
	sort.Slice(snapshot.ThumbnailLinks, func(i, j int) bool { return snapshot.ThumbnailLinks[i].FileID < snapshot.ThumbnailLinks[j].FileID })
	sort.Slice(snapshot.Operations, func(i, j int) bool { return snapshot.Operations[i].ID < snapshot.Operations[j].ID })
	payload, _ := json.Marshal(struct {
		Records []FileRecord  `json:"records"`
		Shares  []ShareRecord `json:"shares"`
	}{snapshot.Records, snapshot.Shares})
	digest := sha256.Sum256(payload)
	snapshot.SourceSHA256 = hex.EncodeToString(digest[:])
	snapshot.SourceGeneration = time.Now().UTC().UnixNano()
	return snapshot, nil
}
