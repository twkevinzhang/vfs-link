package db

import (
	"context"
	"encoding/json"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"time"
)

type treeV4TrashManifest struct {
	Version      int        `json:"version"`
	ID           string     `json:"id"`
	Root         FileRecord `json:"root"`
	Node         treeV4Node `json:"node"`
	Deleting     bool       `json:"deleting,omitempty"`
	CleanupKeys  []string   `json:"cleanupKeys,omitempty"`
	DeletedFiles int64      `json:"deletedFiles,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

func (n *treeV4Namespace) trashManifestKeyV4(id string) string {
	return n.store.prefix + "/v4/trash/manifests/" + encodeTreeSegment(id) + ".json"
}

func (n *treeV4Namespace) readTrashManifest(ctx context.Context, id string) (treeV4TrashManifest, bool, error) {
	raw, _, found, err := n.visibleEnvelope(ctx, n.trashManifestKeyV4(id))
	if err != nil || !found {
		return treeV4TrashManifest{}, found, err
	}
	var manifest treeV4TrashManifest
	err = json.Unmarshal(raw, &manifest)
	return manifest, true, err
}

func (n *treeV4Namespace) trashDelta(ctx context.Context, node treeV4Node, direction int64) (MetadataStats, error) {
	records, _, err := n.subtreeMutationPlan(ctx, node, "")
	if err != nil {
		return MetadataStats{}, err
	}
	var delta MetadataStats
	for _, record := range records {
		if record.IsDirectory {
			delta.LogicalDirs += direction
		} else {
			delta.LogicalFiles += direction
			delta.LogicalBytes += direction * record.Size
			delta.PhysicalObjects += direction
			delta.PhysicalBytes += direction * record.Size
		}
	}
	return delta, nil
}

func (n *treeV4Namespace) subtreeMutationPlan(ctx context.Context, node treeV4Node, path string) ([]FileRecord, []string, error) {
	var records []FileRecord
	var resources []string
	var walk func(treeV4Node, string) error
	walk = func(current treeV4Node, currentPath string) error {
		if current.IsDirectory {
			for shardID := 0; shardID < n.shardCount; shardID++ {
				resources = append(resources, fmt.Sprintf("directory:%s:shard:%03d", current.NodeID, shardID))
			}
			entries, err := n.store.readV4DirectoryEntries(ctx, current.NodeID)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err = walk(entry.Node, joinLogicPath(currentPath, entry.Name)); err != nil {
					return err
				}
			}
		}
		records = append(records, v4FileRecord(currentPath, current))
		return nil
	}
	if err := walk(node, path); err != nil {
		return nil, nil, err
	}
	return records, resources, nil
}

func (n *treeV4Namespace) trashPaths(ctx context.Context, items []TrashPath) ([]FileRecord, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("at least one path is required")
	}
	var roots []FileRecord
	for _, item := range items {
		root, err := n.trashOne(ctx, item)
		if err != nil {
			return roots, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func (n *treeV4Namespace) trashOne(ctx context.Context, item TrashPath) (FileRecord, error) {
	path, err := parseLogicPath(item.Path)
	if err != nil || path == "" || strings.TrimSpace(item.TrashID) == "" {
		return FileRecord{}, fmt.Errorf("path and trash id are required")
	}
	parentPath, name := parentLogicPath(path), pathpkg.Base(path)
	parent, ancestors, found, err := n.resolveDirectoryChain(ctx, parentPath)
	if err != nil || !found {
		return FileRecord{}, ErrNotFound
	}
	shardID := n.shardFor(name)
	rootNode, rootFound, rootErr := n.resolve(ctx, path)
	if rootErr != nil || !rootFound {
		return FileRecord{}, ErrNotFound
	}
	_, subtreeResources, rootErr := n.subtreeMutationPlan(ctx, rootNode, path)
	if rootErr != nil {
		return FileRecord{}, rootErr
	}
	resource := fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, shardID)
	resources := append([]string{"path:" + path, resource, "trash:" + item.TrashID}, subtreeResources...)
	derived := &treeV4DerivedPayload{AncestorDirectoryIDs: ancestors}
	result, _, err := n.transact(ctx, resources, derived, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
		shard, _, readErr := n.readShard(buildCtx, parent.NodeID, shardID)
		if readErr != nil {
			return nil, nil, readErr
		}
		entry, exists := shard.Entries[name]
		if !exists {
			if manifest, manifestFound, manifestErr := n.readTrashManifest(buildCtx, item.TrashID); manifestErr != nil {
				return nil, nil, manifestErr
			} else if manifestFound {
				return nil, manifest.Root, nil
			}
			return nil, nil, ErrNotFound
		}
		_, liveResources, planErr := n.subtreeMutationPlan(buildCtx, entry.Node, path)
		if planErr != nil {
			return nil, nil, planErr
		}
		for _, liveResource := range liveResources {
			if fences[liveResource] == 0 {
				return nil, nil, ErrMetadataConflict
			}
		}
		delta, deltaErr := n.trashDelta(buildCtx, entry.Node, -1)
		if deltaErr != nil {
			return nil, nil, deltaErr
		}
		derived.StatsDelta = delta
		root := v4FileRecord(path, entry.Node)
		at := n.now()
		root.TrashedAt, root.TrashID, root.TrashRoot = &at, item.TrashID, true
		manifest := treeV4TrashManifest{Version: treeV4Version, ID: item.TrashID, Root: root, Node: entry.Node, CreatedAt: at}
		delete(shard.Entries, name)
		return []treeV4Mutation{
			{key: n.shardKey(parent.NodeID, shardID), value: shard, fence: fences[resource]},
			{key: n.trashManifestKeyV4(item.TrashID), value: manifest, requireAbsent: true, fence: fences["trash:"+item.TrashID]},
		}, root, nil
	})
	if err != nil {
		return FileRecord{}, err
	}
	return result.(FileRecord), nil
}

func (n *treeV4Namespace) listTrash(ctx context.Context) ([]FileRecord, error) {
	keys, err := n.store.objects.List(ctx, n.store.prefix+"/v4/trash/manifests/")
	if err != nil {
		return nil, err
	}
	var records []FileRecord
	for _, key := range keys {
		raw, _, found, readErr := n.visibleEnvelope(ctx, key)
		if readErr != nil {
			return nil, readErr
		}
		if !found {
			continue
		}
		var manifest treeV4TrashManifest
		if readErr = json.Unmarshal(raw, &manifest); readErr != nil {
			return nil, readErr
		}
		manifest.Root.TrashDeleting = manifest.Deleting
		records = append(records, manifest.Root)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].TrashedAt.After(*records[j].TrashedAt) })
	return records, nil
}

func (n *treeV4Namespace) collectNodeSubtree(ctx context.Context, node treeV4Node, path, trashID string) ([]FileRecord, error) {
	record := v4FileRecord(path, node)
	now := n.now()
	record.TrashedAt, record.TrashID = &now, trashID
	var records []FileRecord
	if node.IsDirectory {
		for shardID := 0; shardID < n.shardCount; shardID++ {
			shard, _, err := n.readShard(ctx, node.NodeID, shardID)
			if err != nil {
				return nil, err
			}
			for _, entry := range shard.Entries {
				children, childErr := n.collectNodeSubtree(ctx, entry.Node, joinLogicPath(path, entry.Name), trashID)
				if childErr != nil {
					return nil, childErr
				}
				records = append(records, children...)
			}
		}
	}
	records = append(records, record)
	return records, nil
}

func (n *treeV4Namespace) listTrashRecords(ctx context.Context, ids []string) ([]FileRecord, error) {
	wanted := cleanTrashIDs(ids)
	if len(wanted) == 0 {
		roots, err := n.listTrash(ctx)
		if err != nil {
			return nil, err
		}
		for _, root := range roots {
			wanted[root.TrashID] = true
		}
	}
	var records []FileRecord
	for id := range wanted {
		manifest, found, err := n.readTrashManifest(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		subtree, err := n.collectNodeSubtree(ctx, manifest.Node, manifest.Root.LogicPath, id)
		if err != nil {
			return nil, err
		}
		if len(subtree) > 0 {
			subtree[len(subtree)-1].TrashRoot = true
		}
		for index := range subtree {
			subtree[index].TrashedAt = manifest.Root.TrashedAt
			subtree[index].TrashDeleting = manifest.Deleting
		}
		records = append(records, subtree...)
	}
	return records, nil
}

func (n *treeV4Namespace) restoreTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	wanted := cleanTrashIDs(ids)
	if len(wanted) == 0 {
		return nil, fmt.Errorf("at least one trash id is required")
	}
	var restored []FileRecord
	for id := range wanted {
		manifest, found, err := n.readTrashManifest(ctx, id)
		if err != nil || !found {
			return restored, ErrNotFound
		}
		if manifest.Deleting {
			return restored, ErrTrashBusy
		}
		path := manifest.Root.LogicPath
		parentPath, name := parentLogicPath(path), pathpkg.Base(path)
		parent, ancestors, parentFound, parentErr := n.resolveDirectoryChain(ctx, parentPath)
		if parentErr != nil || !parentFound {
			return restored, ErrNotFound
		}
		shardID := n.shardFor(name)
		resource := fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, shardID)
		derived := &treeV4DerivedPayload{AncestorDirectoryIDs: ancestors}
		result, _, txErr := n.transact(ctx, []string{"path:" + path, resource, "trash:" + id}, derived, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
			shard, _, readErr := n.readShard(buildCtx, parent.NodeID, shardID)
			if readErr != nil {
				return nil, nil, readErr
			}
			if _, collision := shard.Entries[name]; collision {
				return nil, nil, ErrPathConflict
			}
			delta, deltaErr := n.trashDelta(buildCtx, manifest.Node, 1)
			if deltaErr != nil {
				return nil, nil, deltaErr
			}
			derived.StatsDelta = delta
			shard.Entries[name] = treeV4DirectoryEntry{Name: name, Node: manifest.Node}
			record := v4FileRecord(path, manifest.Node)
			return []treeV4Mutation{
				{key: n.shardKey(parent.NodeID, shardID), value: shard, fence: fences[resource]},
				{key: n.trashManifestKeyV4(id), delete: true, fence: fences["trash:"+id]},
			}, record, nil
		})
		if txErr != nil {
			return restored, txErr
		}
		restored = append(restored, result.(FileRecord))
	}
	return restored, nil
}

func (n *treeV4Namespace) claimTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	wanted := cleanTrashIDs(ids)
	if len(wanted) == 0 {
		roots, err := n.listTrash(ctx)
		if err != nil {
			return nil, err
		}
		for _, root := range roots {
			wanted[root.TrashID] = true
		}
	}
	var claimed []FileRecord
	for id := range wanted {
		manifest, found, err := n.readTrashManifest(ctx, id)
		if err != nil || !found {
			return claimed, ErrNotFound
		}
		if !manifest.Deleting {
			manifest.Deleting = true
			_, _, err = n.transact(ctx, []string{"trash:" + id}, nil, func(_ context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
				return []treeV4Mutation{{key: n.trashManifestKeyV4(id), value: manifest, fence: fences["trash:"+id]}}, nil, nil
			})
			if err != nil {
				return claimed, err
			}
		}
		records, err := n.collectNodeSubtree(ctx, manifest.Node, manifest.Root.LogicPath, id)
		if err != nil {
			return claimed, err
		}
		for index := range records {
			records[index].TrashDeleting = true
		}
		claimed = append(claimed, records...)
	}
	return claimed, nil
}

func (n *treeV4Namespace) trashCleanupPlan(ctx context.Context, node treeV4Node) ([]string, int64, error) {
	keys := map[string]bool{}
	var files int64
	var walk func(treeV4Node) error
	walk = func(current treeV4Node) error {
		keys[n.nodeKey(current.NodeID)] = true
		keys[n.legacyIDKey(current.LegacyID)] = true
		if !current.IsDirectory {
			files++
			return nil
		}
		shardKeys, err := n.store.objects.List(ctx, fmt.Sprintf("%s/v4/directories/%s/shards/", n.store.prefix, encodeTreeSegment(current.NodeID)))
		if err != nil {
			return err
		}
		for _, key := range shardKeys {
			keys[key] = true
		}
		entries, err := n.store.readV4DirectoryEntries(ctx, current.NodeID)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err = walk(entry.Node); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(node); err != nil {
		return nil, 0, err
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, files, nil
}

func (n *treeV4Namespace) deleteTrash(ctx context.Context, ids []string) (int64, error) {
	wanted := cleanTrashIDs(ids)
	if len(wanted) == 0 {
		roots, err := n.listTrash(ctx)
		if err != nil {
			return 0, err
		}
		for _, root := range roots {
			wanted[root.TrashID] = true
		}
	}
	var deleted int64
	for id := range wanted {
		manifest, found, err := n.readTrashManifest(ctx, id)
		if err != nil {
			return deleted, err
		}
		if !found {
			continue
		}
		if !manifest.Deleting {
			return deleted, ErrTrashBusy
		}
		if len(manifest.CleanupKeys) == 0 {
			manifest.CleanupKeys, manifest.DeletedFiles, err = n.trashCleanupPlan(ctx, manifest.Node)
			if err != nil {
				return deleted, err
			}
			result, _, persistErr := n.transact(ctx, []string{"trash:" + id}, nil, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
				live, liveFound, readErr := n.readTrashManifest(buildCtx, id)
				if readErr != nil || !liveFound {
					return nil, nil, ErrNotFound
				}
				if !live.Deleting {
					return nil, nil, ErrTrashBusy
				}
				if len(live.CleanupKeys) == 0 {
					live.CleanupKeys, live.DeletedFiles = manifest.CleanupKeys, manifest.DeletedFiles
				}
				return []treeV4Mutation{{key: n.trashManifestKeyV4(id), value: live, fence: fences["trash:"+id]}}, live, nil
			})
			if persistErr != nil {
				return deleted, persistErr
			}
			manifest = result.(treeV4TrashManifest)
		}
		// The cleanup plan is persisted before any deletion, so a crash or
		// partial batch is safely retryable even when subtree shards are gone.
		for _, key := range manifest.CleanupKeys {
			if err = n.store.objects.Delete(ctx, key, nil); err != nil {
				return deleted, err
			}
		}
		_, _, err = n.transact(ctx, []string{"trash:" + id}, nil, func(_ context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
			return []treeV4Mutation{{key: n.trashManifestKeyV4(id), delete: true, fence: fences["trash:"+id]}}, nil, nil
		})
		if err != nil {
			return deleted, err
		}
		deleted += manifest.DeletedFiles
	}
	return deleted, nil
}
