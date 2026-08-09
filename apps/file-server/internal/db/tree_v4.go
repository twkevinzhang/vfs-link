package db

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	treeV4Version       = 4
	defaultV4ShardCount = 64
	v4LeaseDuration     = 15 * time.Second
	// A scoped transaction acquires leases sequentially. Budget enough time for
	// every conditional GET/PUT pair so the first lease cannot expire merely
	// while later resources are being fenced.
	v4LeaseResourceBudget      = 500 * time.Millisecond
	maxV4TransactionResources  = 16384
	maxV4DirectoryCacheEntries = 2048
)

type treeV4Namespace struct {
	store            *TreeStore
	shardCount       int
	mutationMode     string
	now              func() time.Time
	rootNode         treeV4Node
	finalizers       sync.WaitGroup
	directoryCacheMu sync.RWMutex
	directoryCache   map[string]treeV4DirectoryCacheEntry
}

type treeV4DirectoryCacheLink struct {
	ParentID   string
	ShardID    int
	Name       string
	ChildID    string
	Generation int64
}

type treeV4DirectoryCacheEntry struct {
	Node  treeV4Node
	Chain []string
	Links []treeV4DirectoryCacheLink
}

type treeV4Root struct {
	Version    int    `json:"version"`
	NodeID     string `json:"nodeId"`
	ShardCount int    `json:"shardCount"`
}

type treeV4Node struct {
	NodeID       string    `json:"nodeId"`
	LegacyID     int       `json:"legacyId"`
	PhysicalHash string    `json:"physicalHash,omitempty"`
	Size         int64     `json:"size,omitempty"`
	IsDirectory  bool      `json:"isDirectory"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type treeV4DirectoryEntry struct {
	Name string     `json:"name"`
	Node treeV4Node `json:"node"`
}

type treeV4DirectoryShard struct {
	DirectoryID string                          `json:"directoryId"`
	Shard       int                             `json:"shard"`
	Entries     map[string]treeV4DirectoryEntry `json:"entries"`
}

type treeV4Pending struct {
	TransactionID string          `json:"transactionId"`
	Value         json.RawMessage `json:"value,omitempty"`
	Delete        bool            `json:"delete,omitempty"`
	Fence         int64           `json:"fence"`
	LeaseUntil    time.Time       `json:"leaseUntil,omitempty"`
}

// treeV4Envelope keeps the previously committed value alongside a prepared
// value. A transaction becomes visible by changing only its manifest to
// committed, so a reader never depends on a multi-object promotion completing.
type treeV4Envelope struct {
	Current json.RawMessage `json:"current,omitempty"`
	Pending *treeV4Pending  `json:"pending,omitempty"`
}

type treeV4Transaction struct {
	ID                   string        `json:"id"`
	Status               string        `json:"status"`
	Owner                string        `json:"owner"`
	Resources            []string      `json:"resources"`
	Participants         []string      `json:"participants,omitempty"`
	StatsDelta           MetadataStats `json:"statsDelta,omitempty"`
	AncestorDirectoryIDs []string      `json:"ancestorDirectoryIds,omitempty"`
	DerivedApplied       bool          `json:"derivedApplied,omitempty"`
	CreatedAt            time.Time     `json:"createdAt"`
	UpdatedAt            time.Time     `json:"updatedAt"`
	LeaseUntil           time.Time     `json:"leaseUntil"`
}

type treeV4Lease struct {
	Owner string    `json:"owner"`
	Until time.Time `json:"until"`
}

type treeV4HeldLease struct {
	resource   string
	key        string
	generation int64
}

type treeV4Mutation struct {
	key                string
	value              any
	delete             bool
	requireAbsent      bool
	fence              int64
	expectedGeneration int64
	enforceGeneration  bool
}

type treeV4DerivedPayload struct {
	StatsDelta           MetadataStats
	AncestorDirectoryIDs []string
}

func treeV4HasDerivedWork(stats MetadataStats, directoryIDs []string) bool {
	return stats.LogicalFiles != 0 || stats.LogicalDirs != 0 || stats.LogicalBytes != 0 ||
		stats.PhysicalObjects != 0 || stats.PhysicalBytes != 0 || len(directoryIDs) != 0
}

func newTreeV4Namespace(store *TreeStore, options TreeV4Options) (*treeV4Namespace, error) {
	shards := options.ShardCount
	if shards == 0 {
		shards = defaultV4ShardCount
	}
	if shards < 1 || shards > 256 || shards&(shards-1) != 0 {
		return nil, fmt.Errorf("v4 shard count must be a power of two between 1 and 256")
	}
	mode := strings.ToLower(strings.TrimSpace(options.MutationMode))
	if mode == "" {
		mode = TreeV4MutationScoped
	}
	if mode != TreeV4MutationScoped && mode != TreeV4MutationGlobal {
		return nil, fmt.Errorf("unsupported v4 mutation mode %q", options.MutationMode)
	}
	return &treeV4Namespace{
		store: store, shardCount: shards, mutationMode: mode,
		now:            func() time.Time { return time.Now().UTC() },
		directoryCache: make(map[string]treeV4DirectoryCacheEntry),
	}, nil
}

func (n *treeV4Namespace) rootKey() string { return n.store.prefix + "/v4/root.json" }
func (n *treeV4Namespace) nodeKey(id string) string {
	return n.store.prefix + "/v4/nodes/" + encodeTreeSegment(id) + ".json"
}
func (n *treeV4Namespace) legacyIDKey(id int) string {
	return fmt.Sprintf("%s/v4/legacy-ids/%d.json", n.store.prefix, id)
}
func (n *treeV4Namespace) shardKey(directoryID string, shard int) string {
	return fmt.Sprintf("%s/v4/directories/%s/shards/%03d.json", n.store.prefix, encodeTreeSegment(directoryID), shard)
}
func (n *treeV4Namespace) transactionKey(id string) string {
	return n.store.prefix + "/v4/transactions/active/" + encodeTreeSegment(id) + ".json"
}
func (n *treeV4Namespace) journalKey(id string) string {
	digest := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%s/v4/journal/%02x/%s.json", n.store.prefix, digest[0], encodeTreeSegment(id))
}
func (n *treeV4Namespace) leaseKey(resource string) string {
	digest := sha256.Sum256([]byte(resource))
	return n.store.prefix + "/v4/leases/" + hex.EncodeToString(digest[:]) + ".json"
}

func (n *treeV4Namespace) ensureSchema(ctx context.Context) error {
	root := treeV4Root{Version: treeV4Version, NodeID: "root", ShardCount: n.shardCount}
	data, _ := marshalTree(root)
	zero := int64(0)
	if _, err := n.store.objects.Put(ctx, n.rootKey(), data, &zero); err != nil && !errorsIsConflict(err) {
		return err
	}
	object, found, err := n.store.objects.Get(ctx, n.rootKey())
	if err != nil || !found {
		return err
	}
	var persisted treeV4Root
	if err = json.Unmarshal(object.Data, &persisted); err != nil {
		return err
	}
	if persisted.Version != treeV4Version || persisted.NodeID != root.NodeID || persisted.ShardCount != n.shardCount {
		return fmt.Errorf("incompatible v4 root: version=%d shards=%d", persisted.Version, persisted.ShardCount)
	}
	rootNode := treeV4Node{NodeID: root.NodeID, IsDirectory: true, UpdatedAt: n.now()}
	if err = n.createInitialEnvelope(ctx, n.nodeKey(root.NodeID), rootNode); err != nil {
		return err
	}
	n.rootNode = rootNode
	return nil
}

func (n *treeV4Namespace) createInitialEnvelope(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	envelope, err := marshalTree(treeV4Envelope{Current: raw})
	if err != nil {
		return err
	}
	zero := int64(0)
	_, err = n.store.objects.Put(ctx, key, envelope, &zero)
	if err != nil && errorsIsConflict(err) {
		return nil
	}
	return err
}

func (n *treeV4Namespace) shardFor(name string) int {
	digest := sha256.Sum256([]byte(name))
	return int(binary.BigEndian.Uint32(digest[:4]) % uint32(n.shardCount))
}

func v4LegacyID(id string) int {
	digest := sha256.Sum256([]byte(id))
	// Keep IDs exactly representable by JavaScript clients while retaining a
	// 52-bit collision domain. A conditional legacy-id index below turns even an
	// extremely unlikely collision into a retryable metadata conflict.
	return int(binary.BigEndian.Uint64(digest[:8])&(1<<52-1)) + 1
}

func (n *treeV4Namespace) readActiveTransaction(ctx context.Context, id string) (treeV4Transaction, int64, bool, error) {
	object, found, err := n.store.objects.Get(ctx, n.transactionKey(id))
	if err != nil {
		return treeV4Transaction{}, 0, false, err
	}
	if !found {
		return treeV4Transaction{}, 0, false, nil
	}
	var transaction treeV4Transaction
	err = json.Unmarshal(object.Data, &transaction)
	return transaction, object.Generation, true, err
}

func (n *treeV4Namespace) readTransactionDecision(ctx context.Context, id string) (treeV4Transaction, int64, bool, error) {
	transaction, generation, found, err := n.readActiveTransaction(ctx, id)
	if err != nil || found {
		return transaction, generation, found, err
	}
	// Active manifests are bounded. A participant left pending by a failed
	// promotion can still resolve against the retained rollback journal.
	object, found, err := n.store.objects.Get(ctx, n.journalKey(id))
	if err != nil || !found {
		return treeV4Transaction{}, 0, found, err
	}
	err = json.Unmarshal(object.Data, &transaction)
	return transaction, object.Generation, true, err
}

func (n *treeV4Namespace) visibleEnvelope(ctx context.Context, key string) (json.RawMessage, int64, bool, error) {
	object, found, err := n.store.objects.Get(ctx, key)
	if err != nil || !found {
		return nil, 0, found, err
	}
	var envelope treeV4Envelope
	if err = json.Unmarshal(object.Data, &envelope); err != nil {
		return nil, 0, true, err
	}
	value := envelope.Current
	if envelope.Pending != nil {
		transaction, _, exists, txErr := n.readTransactionDecision(ctx, envelope.Pending.TransactionID)
		if txErr != nil {
			return nil, 0, true, txErr
		}
		if exists && transaction.Status == "committed" {
			if envelope.Pending.Delete {
				if promotedGeneration, promoteErr := n.promoteEnvelopeStrict(ctx, key, object.Generation, envelope); promoteErr == nil {
					object.Generation = promotedGeneration
				}
				return nil, object.Generation, false, nil
			}
			value = envelope.Pending.Value
			if promotedGeneration, promoteErr := n.promoteEnvelopeStrict(ctx, key, object.Generation, envelope); promoteErr == nil {
				object.Generation = promotedGeneration
			}
		}
	}
	if len(value) == 0 {
		return nil, object.Generation, false, nil
	}
	return value, object.Generation, true, nil
}

func (n *treeV4Namespace) promoteEnvelope(ctx context.Context, key string, generation int64, envelope treeV4Envelope) {
	_, _ = n.promoteEnvelopeStrict(ctx, key, generation, envelope)
}

func (n *treeV4Namespace) promoteEnvelopeStrict(ctx context.Context, key string, generation int64, envelope treeV4Envelope) (int64, error) {
	if envelope.Pending == nil {
		return generation, nil
	}
	if envelope.Pending.Delete {
		envelope.Current = nil
	} else {
		envelope.Current = envelope.Pending.Value
	}
	envelope.Pending = nil
	data, _ := marshalTree(envelope)
	return n.store.objects.Put(ctx, key, data, &generation)
}

func (n *treeV4Namespace) readNode(ctx context.Context, id string) (treeV4Node, bool, error) {
	raw, _, found, err := n.visibleEnvelope(ctx, n.nodeKey(id))
	if err != nil || !found {
		return treeV4Node{}, found, err
	}
	var node treeV4Node
	err = json.Unmarshal(raw, &node)
	return node, true, err
}

func (n *treeV4Namespace) readShard(ctx context.Context, directoryID string, shard int) (treeV4DirectoryShard, bool, error) {
	value, _, found, err := n.readShardSnapshot(ctx, directoryID, shard)
	return value, found, err
}

func (n *treeV4Namespace) readShardSnapshot(ctx context.Context, directoryID string, shard int) (treeV4DirectoryShard, int64, bool, error) {
	raw, generation, found, err := n.visibleEnvelope(ctx, n.shardKey(directoryID, shard))
	if err != nil || !found {
		return treeV4DirectoryShard{DirectoryID: directoryID, Shard: shard, Entries: map[string]treeV4DirectoryEntry{}}, generation, found, err
	}
	var value treeV4DirectoryShard
	if err = json.Unmarshal(raw, &value); err != nil {
		return value, generation, true, err
	}
	if value.Entries == nil {
		value.Entries = map[string]treeV4DirectoryEntry{}
	}
	return value, generation, true, nil
}

func (n *treeV4Namespace) resolve(ctx context.Context, path string) (treeV4Node, bool, error) {
	path, err := parseLogicPath(path)
	if err != nil {
		return treeV4Node{}, false, err
	}
	node := n.rootNode
	if node.NodeID == "" {
		var found bool
		node, found, err = n.readNode(ctx, "root")
		if err != nil || !found {
			return node, found, err
		}
	}
	if path == "" {
		return node, true, nil
	}
	for _, segment := range strings.Split(path, "/") {
		if !node.IsDirectory {
			return treeV4Node{}, false, nil
		}
		shard, _, shardErr := n.readShard(ctx, node.NodeID, n.shardFor(segment))
		if shardErr != nil {
			return treeV4Node{}, false, shardErr
		}
		entry, exists := shard.Entries[segment]
		if !exists {
			return treeV4Node{}, false, nil
		}
		node = entry.Node
	}
	return node, true, nil
}

func (n *treeV4Namespace) resolveDirectoryChain(ctx context.Context, path string) (treeV4Node, []string, bool, error) {
	path, err := parseLogicPath(path)
	if err != nil {
		return treeV4Node{}, nil, false, err
	}
	node := n.rootNode
	if node.NodeID == "" {
		var found bool
		node, found, err = n.readNode(ctx, "root")
		if err != nil || !found {
			return node, nil, found, err
		}
	}
	chain := []string{node.NodeID}
	if path == "" {
		return node, chain, true, nil
	}
	if cached, ok := n.cachedDirectory(path); ok {
		valid, validateErr := n.validateCachedDirectory(ctx, cached)
		if validateErr != nil {
			return treeV4Node{}, nil, false, validateErr
		}
		if valid {
			return cached.Node, append([]string(nil), cached.Chain...), true, nil
		}
		n.directoryCacheMu.Lock()
		delete(n.directoryCache, path)
		n.directoryCacheMu.Unlock()
	}
	links := make([]treeV4DirectoryCacheLink, 0, strings.Count(path, "/")+1)
	for _, segment := range strings.Split(path, "/") {
		shardID := n.shardFor(segment)
		shard, generation, _, shardErr := n.readShardSnapshot(ctx, node.NodeID, shardID)
		if shardErr != nil {
			return treeV4Node{}, nil, false, shardErr
		}
		entry, exists := shard.Entries[segment]
		if !exists || !entry.Node.IsDirectory {
			return treeV4Node{}, nil, false, nil
		}
		links = append(links, treeV4DirectoryCacheLink{
			ParentID: node.NodeID, ShardID: shardID, Name: segment,
			ChildID: entry.Node.NodeID, Generation: generation,
		})
		node = entry.Node
		chain = append(chain, node.NodeID)
	}
	n.cacheDirectory(path, treeV4DirectoryCacheEntry{Node: node, Chain: append([]string(nil), chain...), Links: links})
	return node, chain, true, nil
}

func (n *treeV4Namespace) cachedDirectory(path string) (treeV4DirectoryCacheEntry, bool) {
	n.directoryCacheMu.RLock()
	entry, ok := n.directoryCache[path]
	n.directoryCacheMu.RUnlock()
	return entry, ok
}

func (n *treeV4Namespace) cacheDirectory(path string, entry treeV4DirectoryCacheEntry) {
	n.directoryCacheMu.Lock()
	defer n.directoryCacheMu.Unlock()
	if len(n.directoryCache) >= maxV4DirectoryCacheEntries {
		clear(n.directoryCache)
	}
	n.directoryCache[path] = entry
}

func (n *treeV4Namespace) validateCachedDirectory(ctx context.Context, cached treeV4DirectoryCacheEntry) (bool, error) {
	valid := make([]bool, len(cached.Links))
	tasks := make([]func(context.Context) error, 0, len(cached.Links))
	for index, link := range cached.Links {
		index, link := index, link
		tasks = append(tasks, func(taskCtx context.Context) error {
			shard, generation, found, err := n.readShardSnapshot(taskCtx, link.ParentID, link.ShardID)
			if err != nil {
				return err
			}
			entry, exists := shard.Entries[link.Name]
			valid[index] = found && exists && entry.Node.IsDirectory && entry.Node.NodeID == link.ChildID && generation == link.Generation
			return nil
		})
	}
	if err := runTreeImportTasks(ctx, 16, tasks); err != nil {
		return false, err
	}
	for _, linkValid := range valid {
		if !linkValid {
			return false, nil
		}
	}
	return true, nil
}

func v4FileRecord(path string, node treeV4Node) FileRecord {
	return FileRecord{ID: node.LegacyID, LogicPath: path, PhysicalHash: node.PhysicalHash, Size: node.Size, IsDirectory: node.IsDirectory, UpdatedAt: node.UpdatedAt}
}

func (n *treeV4Namespace) find(ctx context.Context, path string) (FileRecord, bool, error) {
	parsed, err := parseLogicPath(path)
	if err != nil {
		return FileRecord{}, false, err
	}
	if parsed == "" {
		return FileRecord{}, false, nil
	}
	node, found, err := n.resolve(ctx, parsed)
	return v4FileRecord(parsed, node), found, err
}

func (n *treeV4Namespace) listDirectChildren(ctx context.Context, dir string, options DirectChildrenOptions) (DirectChildrenPage, error) {
	dir, err := parseLogicPath(dir)
	if err != nil {
		return DirectChildrenPage{}, err
	}
	parent, found, err := n.resolve(ctx, dir)
	if err != nil {
		return DirectChildrenPage{}, err
	}
	if !found || !parent.IsDirectory {
		return DirectChildrenPage{}, nil
	}
	query := strings.ToLower(strings.TrimSpace(options.Query))
	entries, err := n.store.readV4DirectoryEntries(ctx, parent.NodeID)
	if err != nil {
		return DirectChildrenPage{}, err
	}
	var records []FileRecord
	directoryNodeIDs := map[string]string{}
	for _, entry := range entries {
		if options.DirectoriesOnly && !entry.Node.IsDirectory {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Name), query) {
			continue
		}
		record := v4FileRecord(joinLogicPath(dir, entry.Name), entry.Node)
		records = append(records, record)
		if entry.Node.IsDirectory {
			directoryNodeIDs[record.LogicPath] = entry.Node.NodeID
		}
	}
	sort.Slice(records, func(i, j int) bool { return indexSortKey(records[i]) < indexSortKey(records[j]) })
	page := DirectChildrenPage{Total: len(records)}
	if summary, summaryFound, summaryErr := n.store.DerivedDirectorySummary(ctx, parent.NodeID); summaryErr != nil {
		return DirectChildrenPage{}, summaryErr
	} else if summaryFound {
		page.FolderSummary = summary
	}
	for _, record := range records {
		if !record.IsDirectory {
			page.TotalBytes += record.Size
		}
	}
	start := options.Offset
	if start < 0 {
		start = 0
	}
	if start > len(records) {
		start = len(records)
	}
	end := len(records)
	if options.Limit > 0 && start+options.Limit < end {
		end = start + options.Limit
	}
	page.Records = records[start:end]
	// Directory summaries are per-row enrichment. Hydrate only the requested
	// page after filtering/sorting so limit=100 never triggers thousands of
	// derived snapshot GETs for a large directory.
	hydrateTasks := make([]func(context.Context) error, 0, len(page.Records))
	for index := range page.Records {
		index := index
		if !page.Records[index].IsDirectory {
			continue
		}
		nodeID := directoryNodeIDs[page.Records[index].LogicPath]
		if nodeID == "" {
			continue
		}
		hydrateTasks = append(hydrateTasks, func(taskCtx context.Context) error {
			summary, found, summaryErr := n.store.DerivedDirectorySummary(taskCtx, nodeID)
			if summaryErr != nil {
				return summaryErr
			}
			if !found {
				summary = FolderSummary{}
			}
			page.Records[index].FolderSummary = &summary
			return nil
		})
	}
	if err = runTreeImportTasks(ctx, 16, hydrateTasks); err != nil {
		return DirectChildrenPage{}, err
	}
	return page, nil
}

func (n *treeV4Namespace) listPrefix(ctx context.Context, prefix string) ([]FileRecord, error) {
	prefix, err := parseLogicPrefix(prefix)
	if err != nil {
		return nil, err
	}
	start := strings.TrimSuffix(prefix, "/")
	var out []FileRecord
	if start != "" {
		root, found, findErr := n.find(ctx, start)
		if findErr != nil || !found {
			return nil, findErr
		}
		out = append(out, root)
	}
	var walk func(string) error
	walk = func(dir string) error {
		page, listErr := n.listDirectChildren(ctx, dir, DirectChildrenOptions{})
		if listErr != nil {
			return listErr
		}
		for _, record := range page.Records {
			out = append(out, record)
			if record.IsDirectory {
				if listErr = walk(record.LogicPath); listErr != nil {
					return listErr
				}
			}
		}
		return nil
	}
	if start == "" {
		err = walk("")
	} else if len(out) > 0 && out[0].IsDirectory {
		err = walk(start)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LogicPath < out[j].LogicPath })
	return out, nil
}

func (n *treeV4Namespace) normalizeLeaseResources(resources []string) ([]string, error) {
	unique := map[string]bool{}
	for _, resource := range resources {
		unique[resource] = true
	}
	if n.mutationMode == TreeV4MutationGlobal {
		unique["global"] = true
	}
	resources = resources[:0]
	for resource := range unique {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	if len(resources) > maxV4TransactionResources {
		return nil, fmt.Errorf("v4 transaction needs %d scoped resources; safe maximum is %d", len(resources), maxV4TransactionResources)
	}
	return resources, nil
}

func v4LeaseTTL(resourceCount int) time.Duration {
	if resourceCount < 0 {
		resourceCount = 0
	}
	return v4LeaseDuration + time.Duration(resourceCount)*v4LeaseResourceBudget
}

func (n *treeV4Namespace) acquireLeases(ctx context.Context, transactionID string, resources []string, ttl time.Duration) ([]treeV4HeldLease, error) {
	var held []treeV4HeldLease
	for _, resource := range resources {
		lease, err := n.acquireLease(ctx, transactionID, resource, ttl)
		if err != nil {
			n.releaseLeases(held)
			return nil, err
		}
		held = append(held, lease)
	}
	return held, nil
}

func (n *treeV4Namespace) acquireLease(ctx context.Context, owner, resource string, ttl time.Duration) (treeV4HeldLease, error) {
	key := n.leaseKey(resource)
	for attempt := 0; attempt < treeCASAttempts*8; attempt++ {
		object, found, err := n.store.objects.Get(ctx, key)
		if err != nil {
			return treeV4HeldLease{}, err
		}
		generation := int64(0)
		if found {
			var current treeV4Lease
			if err = json.Unmarshal(object.Data, &current); err != nil {
				return treeV4HeldLease{}, err
			}
			if current.Owner != owner && current.Until.After(n.now()) {
				select {
				case <-ctx.Done():
					return treeV4HeldLease{}, ctx.Err()
				case <-time.After(time.Duration(attempt%8+1) * 5 * time.Millisecond):
					continue
				}
			}
			generation = object.Generation
		}
		data, _ := marshalTree(treeV4Lease{Owner: owner, Until: n.now().Add(ttl)})
		newGeneration, putErr := n.store.objects.Put(ctx, key, data, &generation)
		if putErr == nil {
			return treeV4HeldLease{resource: resource, key: key, generation: newGeneration}, nil
		}
		if !errorsIsConflict(putErr) {
			return treeV4HeldLease{}, putErr
		}
	}
	return treeV4HeldLease{}, ErrMetadataConflict
}

func (n *treeV4Namespace) releaseLeases(leases []treeV4HeldLease) {
	for index := len(leases) - 1; index >= 0; index-- {
		_ = n.store.objects.Delete(context.Background(), leases[index].key, &leases[index].generation)
	}
}

func (n *treeV4Namespace) finishCommittedAsync(transaction treeV4Transaction, generation int64, leases []treeV4HeldLease) {
	n.finalizers.Add(1)
	go func() {
		defer n.finalizers.Done()
		// The manifest CAS is the namespace commit point. Releasing its scoped
		// fences and promoting envelopes are maintenance work; neither needs to
		// extend the client-visible mutation latency.
		n.releaseLeases(leases)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = n.finalizeCommittedTransaction(ctx, transaction, generation)
	}()
}

func (n *treeV4Namespace) finishCommittedRenameAsync(transaction treeV4Transaction, generation int64) {
	n.finalizers.Add(1)
	go func() {
		defer n.finalizers.Done()
		// Same-parent rename shards are mutated again frequently. Keep the
		// committed value in the participant's pending slot and retain its
		// decision in the journal. The next shard CAS can fold that value into
		// Current while preparing its own pending value, avoiding a separate
		// promotion GET/PUT on every rename. Readers continue to resolve the
		// pending value through the archived commit decision.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = n.archiveTransaction(ctx, transaction, generation)
	}()
}

func (n *treeV4Namespace) waitFinalizers() { n.finalizers.Wait() }

func (n *treeV4Namespace) verifyLeases(ctx context.Context, owner string, leases []treeV4HeldLease) error {
	for _, held := range leases {
		object, found, err := n.store.objects.Get(ctx, held.key)
		if err != nil {
			return err
		}
		if !found || object.Generation != held.generation {
			return fmt.Errorf("v4 mutation lease lost: %s", held.resource)
		}
		var lease treeV4Lease
		if err = json.Unmarshal(object.Data, &lease); err != nil {
			return err
		}
		if lease.Owner != owner || !lease.Until.After(n.now()) {
			return fmt.Errorf("v4 mutation lease lost: %s", held.resource)
		}
	}
	return nil
}

func (n *treeV4Namespace) writeTransaction(ctx context.Context, transaction treeV4Transaction, expected int64) (int64, error) {
	transaction.UpdatedAt = n.now()
	data, err := marshalTree(transaction)
	if err != nil {
		return 0, err
	}
	return n.store.objects.Put(ctx, n.transactionKey(transaction.ID), data, &expected)
}

func (n *treeV4Namespace) prepareMutation(ctx context.Context, transactionID string, mutation treeV4Mutation) error {
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		object, found, err := n.store.objects.Get(ctx, mutation.key)
		if err != nil {
			return err
		}
		envelope := treeV4Envelope{}
		generation := int64(0)
		if found {
			generation = object.Generation
			if err = json.Unmarshal(object.Data, &envelope); err != nil {
				return err
			}
			if envelope.Pending != nil && envelope.Pending.TransactionID != transactionID {
				if err = n.normalizeEnvelope(ctx, mutation.key, object.Generation, envelope); err != nil {
					return err
				}
				continue
			}
		}
		if mutation.enforceGeneration && generation != mutation.expectedGeneration {
			return ErrMetadataConflict
		}
		if mutation.requireAbsent && len(envelope.Current) != 0 {
			return ErrMetadataConflict
		}
		var raw json.RawMessage
		if !mutation.delete {
			raw, err = json.Marshal(mutation.value)
			if err != nil {
				return err
			}
		}
		envelope.Pending = &treeV4Pending{TransactionID: transactionID, Value: raw, Delete: mutation.delete, Fence: mutation.fence}
		data, _ := marshalTree(envelope)
		if _, err = n.store.objects.Put(ctx, mutation.key, data, &generation); err == nil {
			return nil
		}
		if !errorsIsConflict(err) {
			return err
		}
	}
	return ErrMetadataConflict
}

func (n *treeV4Namespace) normalizeEnvelope(ctx context.Context, key string, generation int64, envelope treeV4Envelope) error {
	if envelope.Pending == nil {
		return nil
	}
	transaction, txGeneration, found, err := n.readActiveTransaction(ctx, envelope.Pending.TransactionID)
	if err != nil {
		return err
	}
	activeFound := found
	if !found {
		transaction, _, found, err = n.readTransactionDecision(ctx, envelope.Pending.TransactionID)
		if err != nil {
			return err
		}
	}
	if !found && envelope.Pending.LeaseUntil.After(n.now()) {
		// Same-parent rename prepares participants before publishing its atomic
		// decision. Its embedded lease prevents another writer from discarding a
		// live preparation during that short window.
		return ErrMetadataConflict
	}
	if found && transaction.Status == "preparing" && transaction.LeaseUntil.After(n.now()) {
		return ErrMetadataConflict
	}
	if found && transaction.Status == "preparing" && !activeFound {
		return ErrMetadataConflict
	}
	if found && transaction.Status == "preparing" {
		transaction.Status = "aborted"
		if _, writeErr := n.writeTransaction(ctx, transaction, txGeneration); writeErr != nil {
			if !errorsIsConflict(writeErr) {
				return writeErr
			}
			// A committer may have won the manifest CAS while recovery was
			// attempting to abort. Re-read the decision before touching pending
			// participant state.
			transaction, _, found, err = n.readTransactionDecision(ctx, envelope.Pending.TransactionID)
			if err != nil {
				return err
			}
			if !found || transaction.Status == "preparing" {
				return ErrMetadataConflict
			}
		}
	}
	if found && transaction.Status == "committed" {
		if envelope.Pending.Delete {
			envelope.Current = nil
		} else {
			envelope.Current = envelope.Pending.Value
		}
	}
	envelope.Pending = nil
	data, _ := marshalTree(envelope)
	_, err = n.store.objects.Put(ctx, key, data, &generation)
	return err
}

func (n *treeV4Namespace) transact(ctx context.Context, resources []string, derived *treeV4DerivedPayload, build func(context.Context, map[string]int64) ([]treeV4Mutation, any, error)) (any, string, error) {
	return n.transactWithLeases(ctx, resources, derived, true, build)
}

func (n *treeV4Namespace) transactOptimistic(ctx context.Context, resources []string, derived *treeV4DerivedPayload, build func(context.Context, map[string]int64) ([]treeV4Mutation, any, error)) (any, string, error) {
	return n.transactWithLeases(ctx, resources, derived, false, build)
}

func (n *treeV4Namespace) transactWithLeases(ctx context.Context, resources []string, derived *treeV4DerivedPayload, useLeases bool, build func(context.Context, map[string]int64) ([]treeV4Mutation, any, error)) (any, string, error) {
	normalized, err := n.normalizeLeaseResources(resources)
	if err != nil {
		return nil, "", err
	}
	var baseline *treeV4DerivedPayload
	if derived != nil {
		copy := *derived
		copy.AncestorDirectoryIDs = append([]string(nil), derived.AncestorDirectoryIDs...)
		baseline = &copy
	}
	var lastID string
	retrySeed := uuid.NewString()
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		if baseline != nil {
			*derived = *baseline
			derived.AncestorDirectoryIDs = append([]string(nil), baseline.AncestorDirectoryIDs...)
		}
		result, id, transactErr := n.transactOnce(ctx, normalized, derived, useLeases, build)
		lastID = id
		if !errors.Is(transactErr, ErrMetadataConflict) {
			return result, id, transactErr
		}
		if attempt == treeCASAttempts-1 {
			break
		}
		delay := v4TransactionRetryDelay(normalized, retrySeed, attempt)
		select {
		case <-ctx.Done():
			return nil, lastID, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastID, ErrMetadataConflict
}

func v4TransactionRetryDelay(resources []string, seed string, attempt int) time.Duration {
	digest := sha256.Sum256([]byte(strings.Join(resources, "\x00") + "\x00" + seed + fmt.Sprintf("\x00%d", attempt)))
	jitter := time.Duration(digest[0]%25) * time.Millisecond
	return time.Duration(attempt+1)*25*time.Millisecond + jitter
}

func (n *treeV4Namespace) transactOnce(ctx context.Context, resources []string, derived *treeV4DerivedPayload, useLeases bool, build func(context.Context, map[string]int64) ([]treeV4Mutation, any, error)) (any, string, error) {
	id := uuid.NewString()
	var err error
	resources, err = n.normalizeLeaseResources(resources)
	if err != nil {
		return nil, "", err
	}
	leaseTTL := v4LeaseTTL(len(resources))
	transaction := treeV4Transaction{ID: id, Status: "preparing", Owner: id, Resources: append([]string(nil), resources...), CreatedAt: n.now(), LeaseUntil: n.now().Add(leaseTTL)}
	data, _ := marshalTree(transaction)
	zero := int64(0)
	generation, err := n.store.objects.Put(ctx, n.transactionKey(id), data, &zero)
	if err != nil {
		return nil, "", err
	}
	var leases []treeV4HeldLease
	if useLeases {
		leases, err = n.acquireLeases(ctx, id, resources, leaseTTL)
		if err != nil {
			transaction.Status = "aborted"
			_, _ = n.writeTransaction(context.Background(), transaction, generation)
			return nil, "", err
		}
	}
	defer func() { n.releaseLeases(leases) }()
	fences := make(map[string]int64, len(leases))
	for _, lease := range leases {
		fences[lease.resource] = lease.generation
	}
	mutations, result, err := build(ctx, fences)
	if err == nil {
		sort.SliceStable(mutations, func(i, j int) bool { return mutations[i].key < mutations[j].key })
		transaction.Participants = make([]string, 0, len(mutations))
		seen := map[string]bool{}
		for _, mutation := range mutations {
			if !seen[mutation.key] {
				seen[mutation.key] = true
				transaction.Participants = append(transaction.Participants, mutation.key)
			}
		}
		sort.Strings(transaction.Participants)
		if derived != nil {
			transaction.StatsDelta = derived.StatsDelta
			transaction.AncestorDirectoryIDs = normalizeDerivedDirectoryIDs(derived.AncestorDirectoryIDs)
		}
		// A transaction with no aggregate effect does not need to enter the
		// reducer queue. Marking it here lets finalization archive it directly.
		transaction.DerivedApplied = !treeV4HasDerivedWork(transaction.StatsDelta, transaction.AncestorDirectoryIDs)
		generation, err = n.writeTransaction(ctx, transaction, generation)
	}
	if err == nil {
		for _, mutation := range mutations {
			if mutation.fence == 0 && len(leases) > 0 {
				mutation.fence = leases[0].generation
			}
			if err = n.prepareMutation(ctx, id, mutation); err != nil {
				break
			}
		}
	}
	if err == nil && useLeases {
		err = n.verifyLeases(ctx, id, leases)
	}
	if err != nil {
		transaction.Status = "aborted"
		_, _ = n.writeTransaction(context.Background(), transaction, generation)
		return nil, "", err
	}
	transaction.Status = "committed"
	transaction.LeaseUntil = time.Time{}
	if generation, err = n.writeTransaction(ctx, transaction, generation); err != nil {
		// A transport error can happen after the conditional write reached the
		// object store. Resolve the ambiguity before reporting a failed mutation.
		observed, observedGeneration, found, readErr := n.readActiveTransaction(ctx, id)
		if readErr != nil || !found || observed.Status != "committed" {
			return nil, "", err
		}
		transaction, generation, err = observed, observedGeneration, nil
	}
	// Readers resolve pending envelopes through the committed manifest. Release
	// scoped fences and emit/promote in the background so eventual aggregates
	// and representation cleanup do not extend the strong path mutation.
	n.finishCommittedAsync(transaction, generation, leases)
	leases = nil
	return result, id, nil
}

func (n *treeV4Namespace) finalizeCommittedTransaction(ctx context.Context, transaction treeV4Transaction, generation int64) error {
	if !transaction.DerivedApplied {
		if err := n.store.emitDerivedDelta(ctx, transaction.ID, transaction.StatsDelta, transaction.AncestorDirectoryIDs); err != nil {
			return err
		}
	}
	for _, participant := range transaction.Participants {
		object, found, err := n.store.objects.Get(ctx, participant)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		var envelope treeV4Envelope
		if err = json.Unmarshal(object.Data, &envelope); err != nil {
			return err
		}
		if envelope.Pending != nil && envelope.Pending.TransactionID == transaction.ID {
			if _, err = n.promoteEnvelopeStrict(ctx, participant, object.Generation, envelope); err != nil {
				return err
			}
		}
	}
	// The reducer marks DerivedApplied and archives the active manifest only
	// after its idempotent delta has changed the published aggregate state.
	if transaction.DerivedApplied {
		return n.archiveTransaction(ctx, transaction, generation)
	}
	return nil
}

func (n *treeV4Namespace) archiveTransaction(ctx context.Context, transaction treeV4Transaction, generation int64) error {
	payload, _ := marshalTree(transaction)
	zero := int64(0)
	if _, err := n.store.objects.Put(ctx, n.journalKey(transaction.ID), payload, &zero); err != nil && !errorsIsConflict(err) {
		return err
	}
	return n.store.objects.Delete(ctx, n.transactionKey(transaction.ID), &generation)
}

// MarkTreeV4DerivedApplied is called by the reducer after it has durably and
// idempotently applied a transaction delta. It bounds the active-manifest set
// while retaining a hash-sharded rollback journal.
func MarkTreeV4DerivedApplied(ctx context.Context, store Store, transactionID string) error {
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return nil
	}
	namespace := tree.v4
	transaction, generation, found, err := namespace.readActiveTransaction(ctx, transactionID)
	if err != nil {
		return err
	}
	if !found {
		if _, archived, getErr := tree.objects.Get(ctx, namespace.journalKey(transactionID)); getErr != nil || archived {
			return getErr
		}
		return ErrNotFound
	}
	if transaction.Status != "committed" {
		return fmt.Errorf("v4 transaction %s is not committed", transactionID)
	}
	if !transaction.DerivedApplied {
		transaction.DerivedApplied = true
		generation, err = namespace.writeTransaction(ctx, transaction, generation)
		if err != nil {
			return err
		}
	}
	return namespace.archiveTransaction(ctx, transaction, generation)
}

func ArchiveTreeV4Transaction(ctx context.Context, store Store, transactionID string) error {
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return nil
	}
	transaction, generation, found, err := tree.v4.readActiveTransaction(ctx, transactionID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		_, archived, getErr := tree.objects.Get(ctx, tree.v4.journalKey(transactionID))
		if getErr != nil || archived {
			return getErr
		}
		return ErrNotFound
	}
	if transaction.Status == "committed" && !transaction.DerivedApplied {
		return fmt.Errorf("v4 transaction %s derived delta has not been applied", transactionID)
	}
	return tree.v4.archiveTransaction(ctx, transaction, generation)
}

// recoverTransactions resolves terminal participant envelopes and aborts
// expired prepares. It scans transaction manifests rather than the namespace.
func (n *treeV4Namespace) recoverTransactions(ctx context.Context) (int, error) {
	keys, err := n.store.objects.List(ctx, n.store.prefix+"/v4/transactions/active/")
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, key := range keys {
		object, found, getErr := n.store.objects.Get(ctx, key)
		if getErr != nil {
			return recovered, getErr
		}
		if !found {
			continue
		}
		var transaction treeV4Transaction
		if getErr = json.Unmarshal(object.Data, &transaction); getErr != nil {
			return recovered, getErr
		}
		if transaction.Status == "preparing" && !transaction.LeaseUntil.After(n.now()) {
			transaction.Status = "aborted"
			if _, getErr = n.writeTransaction(ctx, transaction, object.Generation); getErr != nil {
				if !errorsIsConflict(getErr) {
					return recovered, getErr
				}
				// Commit and abort race on the manifest. The live manifest is the
				// only authority for whether pending values become visible.
				transaction, _, found, getErr = n.readActiveTransaction(ctx, transaction.ID)
				if getErr != nil {
					return recovered, getErr
				}
				if !found {
					continue
				}
			}
		}
		if transaction.Status != "committed" && transaction.Status != "aborted" {
			continue
		}
		if transaction.Status == "committed" && !transaction.DerivedApplied {
			if emitErr := n.store.emitDerivedDelta(ctx, transaction.ID, transaction.StatsDelta, transaction.AncestorDirectoryIDs); emitErr != nil {
				return recovered, emitErr
			}
		}
		for _, participant := range transaction.Participants {
			participantObject, participantFound, participantErr := n.store.objects.Get(ctx, participant)
			if participantErr != nil {
				return recovered, participantErr
			}
			if !participantFound {
				continue
			}
			var envelope treeV4Envelope
			if participantErr = json.Unmarshal(participantObject.Data, &envelope); participantErr != nil {
				return recovered, participantErr
			}
			if envelope.Pending == nil || envelope.Pending.TransactionID != transaction.ID {
				continue
			}
			if transaction.Status == "committed" {
				n.promoteEnvelope(ctx, participant, participantObject.Generation, envelope)
			} else {
				envelope.Pending = nil
				data, _ := marshalTree(envelope)
				_, _ = n.store.objects.Put(ctx, participant, data, &participantObject.Generation)
			}
		}
		recovered++
		if transaction.Status == "committed" && !transaction.DerivedApplied {
			live, liveGeneration, liveFound, liveErr := n.readActiveTransaction(ctx, transaction.ID)
			if liveErr != nil {
				return recovered, liveErr
			}
			if liveFound {
				if liveErr = n.finalizeCommittedTransaction(ctx, live, liveGeneration); liveErr != nil {
					return recovered, liveErr
				}
			}
		} else if transaction.Status == "committed" || transaction.Status == "aborted" {
			live, liveGeneration, liveFound, liveErr := n.readActiveTransaction(ctx, transaction.ID)
			if liveErr != nil {
				return recovered, liveErr
			}
			if liveFound {
				if liveErr = n.archiveTransaction(ctx, live, liveGeneration); liveErr != nil {
					return recovered, liveErr
				}
			}
		}
	}
	return recovered, nil
}

// RecoverTreeV4Transactions is safe to call from a periodic janitor. It is a
// no-op for v3 stores.
func RecoverTreeV4Transactions(ctx context.Context, store Store) (int, error) {
	tree, ok := store.(*TreeStore)
	if !ok || tree.v4 == nil {
		return 0, nil
	}
	return tree.v4.recoverTransactions(ctx)
}

func v4PathResources(_ string, parent treeV4Node, shard int) []string {
	// The parent shard is the authoritative mapping for every direct child. Its
	// lease already serializes create, replace, rename, move, and delete for a
	// logical path; a second per-path lease only adds GCS round trips.
	return []string{fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, shard)}
}

func (n *treeV4Namespace) replaceFileConditional(ctx context.Context, path, hash string, size int64, expected *string, absent bool) (string, bool, error) {
	return n.replaceFileConditionalExpected(ctx, path, hash, size, expected, nil, absent)
}

func (n *treeV4Namespace) replaceFileConditionalSnapshot(ctx context.Context, path, hash string, size int64, expected *FileSnapshot, absent bool) (string, bool, error) {
	return n.replaceFileConditionalExpected(ctx, path, hash, size, nil, expected, absent)
}

func (n *treeV4Namespace) replaceFileConditionalExpected(ctx context.Context, path, hash string, size int64, expectedHash *string, expectedSnapshot *FileSnapshot, absent bool) (string, bool, error) {
	path, err := parseLogicPath(path)
	if err != nil || path == "" {
		if err == nil {
			err = fmt.Errorf("file path is required")
		}
		return "", false, err
	}
	parentPath, name := parentLogicPath(path), pathpkg.Base(path)
	parent, ancestorIDs, found, err := n.resolveDirectoryChain(ctx, parentPath)
	if err != nil || !found || !parent.IsDirectory {
		if err == nil {
			err = ErrNotFound
		}
		return "", false, err
	}
	shardID := n.shardFor(name)
	newNodeID := uuid.NewString()
	newLegacyID := v4LegacyID(newNodeID)
	resources := append(v4PathResources(path, parent, shardID), fmt.Sprintf("legacy-id:%d", newLegacyID))
	derived := &treeV4DerivedPayload{AncestorDirectoryIDs: ancestorIDs}
	result, transactionID, err := n.transact(ctx, resources, derived, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
		liveParent, exists, buildErr := n.resolve(buildCtx, parentPath)
		if buildErr != nil || !exists || liveParent.NodeID != parent.NodeID {
			return nil, nil, ErrMetadataConflict
		}
		shard, _, buildErr := n.readShard(buildCtx, parent.NodeID, shardID)
		if buildErr != nil {
			return nil, nil, buildErr
		}
		entry, exists := shard.Entries[name]
		if exists && entry.Node.IsDirectory {
			return nil, nil, ErrIsDirectory
		}
		if !exists && (expectedHash != nil || expectedSnapshot != nil) {
			return nil, struct {
				previous string
				matched  bool
			}{"", false}, nil
		}
		if exists && (absent ||
			(expectedHash != nil && entry.Node.PhysicalHash != *expectedHash) ||
			(expectedSnapshot != nil && (entry.Node.LegacyID != expectedSnapshot.ID ||
				entry.Node.PhysicalHash != expectedSnapshot.PhysicalHash ||
				!entry.Node.UpdatedAt.Equal(expectedSnapshot.UpdatedAt)))) {
			return nil, struct {
				previous string
				matched  bool
			}{"", false}, nil
		}
		previous := ""
		node := treeV4Node{NodeID: newNodeID, LegacyID: newLegacyID, PhysicalHash: hash, Size: size, UpdatedAt: n.now()}
		if exists {
			node = entry.Node
			previous = node.PhysicalHash
			derived.StatsDelta.LogicalBytes = size - node.Size
			derived.StatsDelta.PhysicalBytes = size - node.Size
			node.PhysicalHash, node.Size, node.UpdatedAt = hash, size, n.now()
		} else {
			derived.StatsDelta = MetadataStats{LogicalFiles: 1, LogicalBytes: size, PhysicalObjects: 1, PhysicalBytes: size}
		}
		shard.Entries[name] = treeV4DirectoryEntry{Name: name, Node: node}
		resource := fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, shardID)
		mutations := []treeV4Mutation{{key: n.nodeKey(node.NodeID), value: node, fence: fences[resource]}, {key: n.shardKey(parent.NodeID, shardID), value: shard, fence: fences[resource]}}
		if !exists {
			mutations = append(mutations, treeV4Mutation{key: n.legacyIDKey(node.LegacyID), value: struct {
				NodeID string `json:"nodeId"`
			}{node.NodeID}, requireAbsent: true, fence: fences[fmt.Sprintf("legacy-id:%d", node.LegacyID)]})
		}
		return mutations, struct {
			previous string
			matched  bool
		}{previous, true}, nil
	})
	if err != nil {
		return "", false, err
	}
	if result == nil {
		return "", false, nil
	}
	resolved := result.(struct {
		previous string
		matched  bool
	})
	_ = transactionID // consumed by the derived-delta hook when enabled.
	return resolved.previous, resolved.matched, nil
}

func (n *treeV4Namespace) upsertDirectory(ctx context.Context, path string) error {
	path, err := parseLogicPath(path)
	if err != nil || path == "" {
		return err
	}
	parentPath, name := parentLogicPath(path), pathpkg.Base(path)
	parent, ancestorIDs, found, err := n.resolveDirectoryChain(ctx, parentPath)
	if err != nil || !found || !parent.IsDirectory {
		if err == nil {
			err = ErrNotFound
		}
		return err
	}
	shardID := n.shardFor(name)
	newNodeID := uuid.NewString()
	newLegacyID := v4LegacyID(newNodeID)
	resources := append(v4PathResources(path, parent, shardID), fmt.Sprintf("legacy-id:%d", newLegacyID))
	derived := &treeV4DerivedPayload{AncestorDirectoryIDs: ancestorIDs}
	_, _, err = n.transact(ctx, resources, derived, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
		liveParent, exists, buildErr := n.resolve(buildCtx, parentPath)
		if buildErr != nil || !exists || liveParent.NodeID != parent.NodeID {
			return nil, nil, ErrMetadataConflict
		}
		shard, _, buildErr := n.readShard(buildCtx, parent.NodeID, shardID)
		if buildErr != nil {
			return nil, nil, buildErr
		}
		if entry, exists := shard.Entries[name]; exists {
			if !entry.Node.IsDirectory {
				return nil, nil, ErrPathConflict
			}
			return nil, nil, nil
		}
		node := treeV4Node{NodeID: newNodeID, LegacyID: newLegacyID, IsDirectory: true, UpdatedAt: n.now()}
		derived.StatsDelta.LogicalDirs = 1
		shard.Entries[name] = treeV4DirectoryEntry{Name: name, Node: node}
		resource := fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, shardID)
		return []treeV4Mutation{
			{key: n.nodeKey(node.NodeID), value: node, fence: fences[resource]},
			{key: n.shardKey(parent.NodeID, shardID), value: shard, fence: fences[resource]},
			{key: n.legacyIDKey(node.LegacyID), value: struct {
				NodeID string `json:"nodeId"`
			}{node.NodeID}, requireAbsent: true, fence: fences[fmt.Sprintf("legacy-id:%d", node.LegacyID)]},
		}, nil, nil
	})
	return err
}

func (n *treeV4Namespace) deletePath(ctx context.Context, path string) error {
	path, err := parseLogicPath(path)
	if err != nil || path == "" {
		return err
	}
	parentPath, name := parentLogicPath(path), pathpkg.Base(path)
	parent, ancestorIDs, found, err := n.resolveDirectoryChain(ctx, parentPath)
	if err != nil || !found {
		return err
	}
	shardID := n.shardFor(name)
	node, nodeFound, nodeErr := n.resolve(ctx, path)
	if nodeErr != nil {
		return nodeErr
	}
	// Linearize an absent delete at its initial path read. It must not acquire a
	// lease later and accidentally delete a directory created in the meantime.
	if !nodeFound {
		return nil
	}
	resources := v4PathResources(path, parent, shardID)
	resources = append(resources, fmt.Sprintf("legacy-id:%d", node.LegacyID))
	if nodeFound && node.IsDirectory {
		for childShard := 0; childShard < n.shardCount; childShard++ {
			resources = append(resources, fmt.Sprintf("directory:%s:shard:%03d", node.NodeID, childShard))
		}
	}
	derived := &treeV4DerivedPayload{AncestorDirectoryIDs: ancestorIDs}
	_, _, err = n.transact(ctx, resources, derived, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
		liveParent, exists, buildErr := n.resolve(buildCtx, parentPath)
		if buildErr != nil || !exists || liveParent.NodeID != parent.NodeID {
			return nil, nil, ErrMetadataConflict
		}
		shard, _, buildErr := n.readShard(buildCtx, parent.NodeID, shardID)
		if buildErr != nil {
			return nil, nil, buildErr
		}
		entry, exists := shard.Entries[name]
		if !exists {
			return nil, nil, ErrMetadataConflict
		}
		if entry.Node.NodeID != node.NodeID {
			return nil, nil, ErrMetadataConflict
		}
		if entry.Node.IsDirectory {
			page, listErr := n.listDirectChildren(buildCtx, path, DirectChildrenOptions{Limit: 1})
			if listErr != nil {
				return nil, nil, listErr
			}
			if page.Total > 0 {
				return nil, nil, fmt.Errorf("directory not empty")
			}
		}
		if entry.Node.IsDirectory {
			derived.StatsDelta.LogicalDirs = -1
		} else {
			derived.StatsDelta = MetadataStats{LogicalFiles: -1, LogicalBytes: -entry.Node.Size, PhysicalObjects: -1, PhysicalBytes: -entry.Node.Size}
		}
		delete(shard.Entries, name)
		resource := fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, shardID)
		return []treeV4Mutation{
			{key: n.nodeKey(entry.Node.NodeID), delete: true, fence: fences[resource]},
			{key: n.legacyIDKey(entry.Node.LegacyID), delete: true, fence: fences[fmt.Sprintf("legacy-id:%d", entry.Node.LegacyID)]},
			{key: n.shardKey(parent.NodeID, shardID), value: shard, fence: fences[resource]},
		}, nil, nil
	})
	return err
}

type treeV4ShardMutationSnapshot struct {
	key        string
	generation int64
	current    json.RawMessage
	shard      treeV4DirectoryShard
}

func (n *treeV4Namespace) readShardMutationSnapshot(ctx context.Context, directoryID string, shardID int) (treeV4ShardMutationSnapshot, error) {
	key := n.shardKey(directoryID, shardID)
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		object, found, err := n.store.objects.Get(ctx, key)
		if err != nil {
			return treeV4ShardMutationSnapshot{}, err
		}
		snapshot := treeV4ShardMutationSnapshot{
			key: key,
			shard: treeV4DirectoryShard{
				DirectoryID: directoryID,
				Shard:       shardID,
				Entries:     map[string]treeV4DirectoryEntry{},
			},
		}
		if !found {
			return snapshot, nil
		}
		snapshot.generation = object.Generation
		var envelope treeV4Envelope
		if err = json.Unmarshal(object.Data, &envelope); err != nil {
			return treeV4ShardMutationSnapshot{}, err
		}
		if envelope.Pending != nil {
			transaction, _, decisionFound, decisionErr := n.readTransactionDecision(ctx, envelope.Pending.TransactionID)
			if decisionErr != nil {
				return treeV4ShardMutationSnapshot{}, decisionErr
			}
			if decisionFound && (transaction.Status == "committed" || transaction.Status == "aborted") {
				if transaction.Status == "committed" {
					if envelope.Pending.Delete {
						envelope.Current = nil
					} else {
						envelope.Current = envelope.Pending.Value
					}
				}
				// Preserve the observed object generation. prepareShardSnapshot
				// replaces this resolved representation and its own Pending value
				// with one conditional write, so no standalone promotion is needed.
				snapshot.current = append(json.RawMessage(nil), envelope.Current...)
				if len(snapshot.current) != 0 {
					if err = json.Unmarshal(snapshot.current, &snapshot.shard); err != nil {
						return treeV4ShardMutationSnapshot{}, err
					}
					if snapshot.shard.Entries == nil {
						snapshot.shard.Entries = map[string]treeV4DirectoryEntry{}
					}
				}
				return snapshot, nil
			}
			if !decisionFound && envelope.Pending.LeaseUntil.After(n.now()) {
				return treeV4ShardMutationSnapshot{}, ErrMetadataConflict
			}
			if err = n.normalizeEnvelope(ctx, key, object.Generation, envelope); err != nil {
				return treeV4ShardMutationSnapshot{}, err
			}
			// A prior committed transaction can leave its representation pending
			// until the asynchronous finalizer runs. Once normalization succeeds,
			// re-read the new generation in this attempt instead of aborting a new
			// transaction and adding a synthetic conflict/backoff cycle.
			continue
		}
		snapshot.current = append(json.RawMessage(nil), envelope.Current...)
		if len(snapshot.current) != 0 {
			if err = json.Unmarshal(snapshot.current, &snapshot.shard); err != nil {
				return treeV4ShardMutationSnapshot{}, err
			}
			if snapshot.shard.Entries == nil {
				snapshot.shard.Entries = map[string]treeV4DirectoryEntry{}
			}
		}
		return snapshot, nil
	}
	return treeV4ShardMutationSnapshot{}, ErrMetadataConflict
}

func (n *treeV4Namespace) prepareShardSnapshot(ctx context.Context, transactionID string, leaseUntil time.Time, snapshot treeV4ShardMutationSnapshot, value treeV4DirectoryShard) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	envelope := treeV4Envelope{
		Current: snapshot.current,
		Pending: &treeV4Pending{TransactionID: transactionID, Value: raw, LeaseUntil: leaseUntil},
	}
	data, _ := marshalTree(envelope)
	_, err = n.store.objects.Put(ctx, snapshot.key, data, &snapshot.generation)
	return err
}

func (n *treeV4Namespace) publishRenameDecision(ctx context.Context, transaction treeV4Transaction) (int64, error) {
	transaction.UpdatedAt = n.now()
	data, err := marshalTree(transaction)
	if err != nil {
		return 0, err
	}
	zero := int64(0)
	return n.store.objects.Put(ctx, n.transactionKey(transaction.ID), data, &zero)
}

func (n *treeV4Namespace) renameSameParentOptimistic(ctx context.Context, parentPath, fromName, toName string) (treeV4Node, error) {
	node, err := n.renameSameParentOptimisticAttempts(ctx, parentPath, fromName, toName)
	if !errors.Is(err, ErrMetadataConflict) {
		return node, err
	}

	// Keep the normal rename path lease-free. Under a true same-path race,
	// however, contenders can each prepare a different participant shard and
	// exhaust optimistic retries without any transaction reaching commit. Once
	// that happens, serialize only this shard pair and retry the same protocol so
	// the race has one durable winner instead of a distributed livelock.
	parent, _, found, resolveErr := n.resolveDirectoryChain(ctx, parentPath)
	if resolveErr != nil {
		return treeV4Node{}, resolveErr
	}
	if !found || !parent.IsDirectory {
		return treeV4Node{}, ErrNotFound
	}
	resources, resolveErr := n.normalizeLeaseResources([]string{
		fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, n.shardFor(fromName)),
		fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, n.shardFor(toName)),
	})
	if resolveErr != nil {
		return treeV4Node{}, resolveErr
	}
	owner := uuid.NewString()
	leases, acquireErr := n.acquireLeases(ctx, owner, resources, v4LeaseTTL(len(resources)))
	if acquireErr != nil {
		return treeV4Node{}, acquireErr
	}
	defer n.releaseLeases(leases)
	return n.renameSameParentOptimisticAttempts(ctx, parentPath, fromName, toName)
}

func (n *treeV4Namespace) renameSameParentOptimisticAttempts(ctx context.Context, parentPath, fromName, toName string) (treeV4Node, error) {
	fromShardID, toShardID := n.shardFor(fromName), n.shardFor(toName)
	retrySeed := uuid.NewString()
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		parent, _, found, err := n.resolveDirectoryChain(ctx, parentPath)
		if err != nil {
			return treeV4Node{}, err
		}
		if !found || !parent.IsDirectory {
			return treeV4Node{}, ErrNotFound
		}
		resources, err := n.normalizeLeaseResources([]string{
			fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, fromShardID),
			fmt.Sprintf("directory:%s:shard:%03d", parent.NodeID, toShardID),
		})
		if err != nil {
			return treeV4Node{}, err
		}
		id := uuid.NewString()
		now := n.now()
		participantKeys := []string{n.shardKey(parent.NodeID, fromShardID)}
		if toShardID != fromShardID {
			participantKeys = append(participantKeys, n.shardKey(parent.NodeID, toShardID))
		}
		sort.Strings(participantKeys)
		transaction := treeV4Transaction{
			ID: id, Status: "preparing", Owner: id, Resources: resources,
			Participants: participantKeys, DerivedApplied: true,
			CreatedAt: now, UpdatedAt: now, LeaseUntil: now.Add(v4LeaseTTL(len(resources))),
		}
		snapshots := make([]treeV4ShardMutationSnapshot, 2)
		readErrors := make([]error, 2)
		var reads sync.WaitGroup
		reads.Add(1)
		go func() {
			defer reads.Done()
			snapshots[0], readErrors[0] = n.readShardMutationSnapshot(ctx, parent.NodeID, fromShardID)
		}()
		if fromShardID == toShardID {
			reads.Wait()
			snapshots[1], readErrors[1] = snapshots[0], readErrors[0]
		} else {
			reads.Add(1)
			go func() {
				defer reads.Done()
				snapshots[1], readErrors[1] = n.readShardMutationSnapshot(ctx, parent.NodeID, toShardID)
			}()
			reads.Wait()
		}
		abort := func() {
			transaction.Status = "aborted"
			transaction.LeaseUntil = time.Time{}
			abortCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			generation, publishErr := n.publishRenameDecision(abortCtx, transaction)
			if publishErr == nil {
				n.finishCommittedRenameAsync(transaction, generation)
			}
		}
		if readErrors[0] != nil || readErrors[1] != nil {
			err = errors.Join(readErrors...)
			if !errors.Is(err, ErrMetadataConflict) {
				return treeV4Node{}, err
			}
		} else {
			entry, exists := snapshots[0].shard.Entries[fromName]
			if !exists {
				return treeV4Node{}, ErrNotFound
			}
			if _, collision := snapshots[1].shard.Entries[toName]; collision {
				return treeV4Node{}, ErrPathConflict
			}
			values := []treeV4DirectoryShard{snapshots[0].shard, snapshots[1].shard}
			if fromShardID == toShardID {
				delete(values[0].Entries, fromName)
				entry.Name = toName
				values[0].Entries[toName] = entry
				values[1] = values[0]
				snapshots = snapshots[:1]
				values = values[:1]
			} else {
				delete(values[0].Entries, fromName)
				entry.Name = toName
				values[1].Entries[toName] = entry
			}
			type participant struct {
				snapshot treeV4ShardMutationSnapshot
				value    treeV4DirectoryShard
			}
			participants := make([]participant, len(snapshots))
			for index := range snapshots {
				participants[index] = participant{snapshot: snapshots[index], value: values[index]}
			}
			sort.Slice(participants, func(i, j int) bool { return participants[i].snapshot.key < participants[j].snapshot.key })
			prepareErrors := make([]error, len(participants))
			prepareTasks := make([]func(context.Context) error, 0, len(participants))
			for index, item := range participants {
				index, item := index, item
				prepareTasks = append(prepareTasks, func(taskCtx context.Context) error {
					prepareErrors[index] = n.prepareShardSnapshot(taskCtx, id, transaction.LeaseUntil, item.snapshot, item.value)
					return nil
				})
			}
			if runErr := runTreeImportTasks(ctx, len(prepareTasks), prepareTasks); runErr != nil {
				err = runErr
			} else {
				err = errors.Join(prepareErrors...)
			}
			if err != nil {
				abort()
				if !errorsIsConflict(err) {
					return treeV4Node{}, err
				}
			} else {
				transaction.Status = "committed"
				transaction.LeaseUntil = time.Time{}
				generation, err := n.publishRenameDecision(ctx, transaction)
				if err != nil {
					observed, observedGeneration, observedFound, readErr := n.readActiveTransaction(ctx, id)
					if readErr != nil || !observedFound || observed.Status != "committed" {
						abort()
						return treeV4Node{}, err
					}
					transaction, generation = observed, observedGeneration
				}
				n.finishCommittedRenameAsync(transaction, generation)
				return entry.Node, nil
			}
		}
		if attempt == treeCASAttempts-1 {
			break
		}
		delay := v4TransactionRetryDelay(resources, retrySeed, attempt)
		select {
		case <-ctx.Done():
			return treeV4Node{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	return treeV4Node{}, ErrMetadataConflict
}

func (n *treeV4Namespace) renamePath(ctx context.Context, from, to string) error {
	from, to = cleanLogicPath(from), cleanLogicPath(to)
	if from == "" || to == "" || from == to || strings.HasPrefix(to, withTrailingSlash(from)) {
		return ErrInvalidMove
	}
	fromParentPath, toParentPath := parentLogicPath(from), parentLogicPath(to)
	if fromParentPath == toParentPath {
		_, err := n.renameSameParentOptimistic(ctx, fromParentPath, pathpkg.Base(from), pathpkg.Base(to))
		return err
	}
	fromParent, fromAncestors, found, err := n.resolveDirectoryChain(ctx, fromParentPath)
	if err != nil || !found {
		return ErrNotFound
	}
	toParent, toAncestors := fromParent, append([]string(nil), fromAncestors...)
	if fromParentPath != toParentPath {
		toParent, toAncestors, found, err = n.resolveDirectoryChain(ctx, toParentPath)
		if err != nil || !found || !toParent.IsDirectory {
			return ErrNotFound
		}
	}
	fromName, toName := pathpkg.Base(from), pathpkg.Base(to)
	fromShardID, toShardID := n.shardFor(fromName), n.shardFor(toName)
	resources := append(v4PathResources(from, fromParent, fromShardID), v4PathResources(to, toParent, toShardID)...)
	derived := &treeV4DerivedPayload{}
	if fromParent.NodeID != toParent.NodeID {
		derived.AncestorDirectoryIDs = append(fromAncestors, toAncestors...)
	}
	_, _, err = n.transact(ctx, resources, derived, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
		liveFromParent, fromExists, buildErr := n.resolve(buildCtx, fromParentPath)
		if buildErr != nil || !fromExists || liveFromParent.NodeID != fromParent.NodeID {
			return nil, nil, ErrMetadataConflict
		}
		if fromParentPath != toParentPath {
			liveToParent, toExists, resolveErr := n.resolve(buildCtx, toParentPath)
			if resolveErr != nil || !toExists || liveToParent.NodeID != toParent.NodeID {
				return nil, nil, ErrMetadataConflict
			}
		}
		fromShard, fromGeneration, _, buildErr := n.readShardSnapshot(buildCtx, fromParent.NodeID, fromShardID)
		if buildErr != nil {
			return nil, nil, buildErr
		}
		entry, exists := fromShard.Entries[fromName]
		if !exists {
			return nil, nil, ErrNotFound
		}
		toShard := fromShard
		toGeneration := fromGeneration
		sameShard := fromParent.NodeID == toParent.NodeID && fromShardID == toShardID
		if !sameShard {
			toShard, toGeneration, _, buildErr = n.readShardSnapshot(buildCtx, toParent.NodeID, toShardID)
			if buildErr != nil {
				return nil, nil, buildErr
			}
		}
		if _, collision := toShard.Entries[toName]; collision {
			return nil, nil, ErrPathConflict
		}
		delete(fromShard.Entries, fromName)
		entry.Name = toName
		toShard.Entries[toName] = entry
		if sameShard {
			fromShard = toShard
			delete(fromShard.Entries, fromName)
			fromShard.Entries[toName] = entry
			resource := fmt.Sprintf("directory:%s:shard:%03d", fromParent.NodeID, fromShardID)
			return []treeV4Mutation{{key: n.shardKey(fromParent.NodeID, fromShardID), value: fromShard, fence: fences[resource], expectedGeneration: fromGeneration, enforceGeneration: true}}, nil, nil
		}
		fromResource := fmt.Sprintf("directory:%s:shard:%03d", fromParent.NodeID, fromShardID)
		toResource := fmt.Sprintf("directory:%s:shard:%03d", toParent.NodeID, toShardID)
		return []treeV4Mutation{
			{key: n.shardKey(fromParent.NodeID, fromShardID), value: fromShard, fence: fences[fromResource], expectedGeneration: fromGeneration, enforceGeneration: true},
			{key: n.shardKey(toParent.NodeID, toShardID), value: toShard, fence: fences[toResource], expectedGeneration: toGeneration, enforceGeneration: true},
		}, nil, nil
	})
	return err
}

func (n *treeV4Namespace) movePaths(ctx context.Context, paths []string, destination string) ([]FileRecord, error) {
	roots, err := normalizeRoots(paths)
	if err != nil {
		return nil, err
	}
	destination, err = parseLogicPath(destination)
	if err != nil {
		return nil, err
	}
	targetParent, targetAncestors, found, err := n.resolveDirectoryChain(ctx, destination)
	if err != nil || !found || !targetParent.IsDirectory {
		if err == nil {
			err = ErrNotFound
		}
		return nil, err
	}
	type plannedMove struct {
		from, to               string
		fromParent, toParent   treeV4Node
		fromAncestors          []string
		fromName, toName       string
		fromShardID, toShardID int
	}
	plans := make([]plannedMove, 0, len(roots))
	var resources []string
	for _, root := range roots {
		node, exists, findErr := n.resolve(ctx, root)
		if findErr != nil || !exists {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, root)
		}
		target := joinLogicPath(destination, pathpkg.Base(root))
		if target == root || (node.IsDirectory && strings.HasPrefix(target, withTrailingSlash(root))) {
			return nil, ErrInvalidMove
		}
		fromParent, fromAncestors, parentExists, parentErr := n.resolveDirectoryChain(ctx, parentLogicPath(root))
		if parentErr != nil || !parentExists {
			return nil, ErrMetadataConflict
		}
		plan := plannedMove{
			from: root, to: target, fromParent: fromParent, toParent: targetParent,
			fromAncestors: fromAncestors, fromName: pathpkg.Base(root), toName: pathpkg.Base(target),
		}
		plan.fromShardID = n.shardFor(plan.fromName)
		plan.toShardID = n.shardFor(plan.toName)
		plans = append(plans, plan)
		resources = append(resources, v4PathResources(plan.from, plan.fromParent, plan.fromShardID)...)
		resources = append(resources, v4PathResources(plan.to, plan.toParent, plan.toShardID)...)
	}
	derived := &treeV4DerivedPayload{AncestorDirectoryIDs: targetAncestors}
	for _, plan := range plans {
		derived.AncestorDirectoryIDs = append(derived.AncestorDirectoryIDs, plan.fromAncestors...)
	}
	result, _, err := n.transact(ctx, resources, derived, func(buildCtx context.Context, fences map[string]int64) ([]treeV4Mutation, any, error) {
		shards := map[string]treeV4DirectoryShard{}
		shardInfo := map[string]struct {
			directoryID string
			shardID     int
			resource    string
		}{}
		load := func(directoryID string, shardID int) (treeV4DirectoryShard, error) {
			key := n.shardKey(directoryID, shardID)
			if shard, ok := shards[key]; ok {
				return shard, nil
			}
			shard, _, loadErr := n.readShard(buildCtx, directoryID, shardID)
			if loadErr == nil {
				shards[key] = shard
				shardInfo[key] = struct {
					directoryID string
					shardID     int
					resource    string
				}{directoryID, shardID, fmt.Sprintf("directory:%s:shard:%03d", directoryID, shardID)}
			}
			return shard, loadErr
		}
		var moved []FileRecord
		for _, plan := range plans {
			liveFromParent, fromExists, resolveErr := n.resolve(buildCtx, parentLogicPath(plan.from))
			if resolveErr != nil || !fromExists || liveFromParent.NodeID != plan.fromParent.NodeID {
				return nil, nil, ErrMetadataConflict
			}
			liveToParent, toExists, resolveErr := n.resolve(buildCtx, parentLogicPath(plan.to))
			if resolveErr != nil || !toExists || liveToParent.NodeID != plan.toParent.NodeID {
				return nil, nil, ErrMetadataConflict
			}
			fromShard, loadErr := load(plan.fromParent.NodeID, plan.fromShardID)
			if loadErr != nil {
				return nil, nil, loadErr
			}
			entry, exists := fromShard.Entries[plan.fromName]
			if !exists {
				return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, plan.from)
			}
			toShard, loadErr := load(plan.toParent.NodeID, plan.toShardID)
			if loadErr != nil {
				return nil, nil, loadErr
			}
			if _, collision := toShard.Entries[plan.toName]; collision {
				return nil, nil, fmt.Errorf("%w: %s", ErrPathConflict, plan.to)
			}
			delete(fromShard.Entries, plan.fromName)
			entry.Name = plan.toName
			toShard.Entries[plan.toName] = entry
			shards[n.shardKey(plan.fromParent.NodeID, plan.fromShardID)] = fromShard
			shards[n.shardKey(plan.toParent.NodeID, plan.toShardID)] = toShard
			moved = append(moved, v4FileRecord(plan.to, entry.Node))
		}
		mutations := make([]treeV4Mutation, 0, len(shards))
		for key, shard := range shards {
			info := shardInfo[key]
			mutations = append(mutations, treeV4Mutation{key: key, value: shard, fence: fences[info.resource]})
		}
		sort.Slice(mutations, func(i, j int) bool { return mutations[i].key < mutations[j].key })
		return mutations, moved, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]FileRecord), nil
}

func (n *treeV4Namespace) runOperation(ctx context.Context, id string) (OperationRecord, error) {
	operation, generation, found, err := n.store.loadOperation(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return operation, err
	}
	if operation.Status.Terminal() {
		return operation, nil
	}
	if operation.HasActiveLease(n.now()) {
		return operation, fmt.Errorf("operation is already running")
	}
	if !operation.Type.SupportedByTreeV4() {
		return operation, ErrV4Unsupported
	}
	if !operation.Status.CanTransitionTo(OperationStatusRunning) {
		return operation, fmt.Errorf("operation cannot transition from %q to running", operation.Status)
	}
	operation.Status = OperationStatusRunning
	operation.LeaseOwner = uuid.NewString()
	owner := operation.LeaseOwner
	leaseUntil := n.now().Add(2 * time.Minute)
	operation.LeaseUntil = &leaseUntil
	if generation, err = n.store.saveOperationCAS(ctx, operation, generation); err != nil {
		return operation, err
	}
	var records []FileRecord
	if operation.Type == OperationTypeMove {
		records, err = n.movePaths(ctx, operation.Paths, operation.Destination)
	} else if operation.Type == OperationTypeRename && len(operation.Paths) != 1 {
		err = fmt.Errorf("invalid rename operation")
	} else if operation.Type == OperationTypeRename {
		err = n.renamePath(ctx, operation.Paths[0], operation.Destination)
		if err == nil {
			if record, found, findErr := n.find(ctx, operation.Destination); findErr != nil {
				err = findErr
			} else if found {
				records = []FileRecord{record}
			}
		}
	} else if operation.Type == OperationTypeTrash {
		records, err = n.trashPaths(ctx, operation.TrashItems)
	} else {
		records, err = n.restoreTrash(ctx, operation.TrashIDs)
	}
	current, currentGeneration, currentFound, loadErr := n.store.loadOperation(ctx, id)
	if loadErr != nil || !currentFound {
		return operation, loadErr
	}
	if current.Status == OperationStatusCompleted {
		return current, nil
	}
	if current.LeaseOwner != owner {
		return current, fmt.Errorf("operation lease lost")
	}
	current.LeaseOwner = ""
	current.LeaseUntil = nil
	if err != nil {
		current.Status = OperationStatusPending
		if treeV4OperationErrorIsTerminal(err) {
			current.Status = OperationStatusFailed
		}
		current.Error = err.Error()
	} else {
		current.Status = OperationStatusCompleted
		current.Result = records
		current.Progress = len(records)
		current.Total = len(records)
		current.Error = ""
	}
	_, saveErr := n.store.saveOperationCAS(ctx, current, currentGeneration)
	if saveErr != nil {
		return current, saveErr
	}
	return current, err
}

func treeV4OperationErrorIsTerminal(err error) bool {
	return errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrPathConflict) ||
		errors.Is(err, ErrV4Unsupported)
}
