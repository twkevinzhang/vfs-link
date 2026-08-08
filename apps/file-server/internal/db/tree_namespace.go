package db

import "context"

// treeNamespace is the single dispatch boundary between the legacy V3 object
// layout and the transaction-backed V4 namespace. Methods included here have
// equivalent public Store contracts in both implementations.
type treeNamespace interface {
	ensureSchema(context.Context) error
	find(context.Context, string) (FileRecord, bool, error)
	listDirectChildren(context.Context, string, DirectChildrenOptions) (DirectChildrenPage, error)
	listPrefix(context.Context, string) ([]FileRecord, error)
	replaceFileConditional(context.Context, string, string, int64, *string, bool) (string, bool, error)
	upsertDirectory(context.Context, string) error
	deletePath(context.Context, string) error
	runOperation(context.Context, string) (OperationRecord, error)
	movePaths(context.Context, []string, string) ([]FileRecord, error)
	renamePath(context.Context, string, string) error
	trashPaths(context.Context, []TrashPath) ([]FileRecord, error)
	listTrash(context.Context) ([]FileRecord, error)
	listTrashRecords(context.Context, []string) ([]FileRecord, error)
	restoreTrash(context.Context, []string) ([]FileRecord, error)
	claimTrash(context.Context, []string) ([]FileRecord, error)
	deleteTrash(context.Context, []string) (int64, error)
}

type treeV3Namespace struct {
	store *TreeStore
}

func (n *treeV3Namespace) ensureSchema(ctx context.Context) error {
	return n.store.ensureSchemaV3(ctx)
}

func (n *treeV3Namespace) find(ctx context.Context, path string) (FileRecord, bool, error) {
	return n.store.findV3(ctx, path)
}

func (n *treeV3Namespace) listDirectChildren(ctx context.Context, dir string, options DirectChildrenOptions) (DirectChildrenPage, error) {
	return n.store.listDirectChildrenV3(ctx, dir, options)
}

func (n *treeV3Namespace) listPrefix(ctx context.Context, prefix string) ([]FileRecord, error) {
	return n.store.listPrefixV3(ctx, prefix)
}

func (n *treeV3Namespace) replaceFileConditional(ctx context.Context, path, hash string, size int64, expected *string, absent bool) (string, bool, error) {
	return n.store.replaceFileConditionalV3(ctx, path, hash, size, expected, absent)
}

func (n *treeV3Namespace) upsertDirectory(ctx context.Context, path string) error {
	return n.store.upsertDirectoryV3(ctx, path)
}

func (n *treeV3Namespace) deletePath(ctx context.Context, path string) error {
	return n.store.deletePathV3(ctx, path)
}

func (n *treeV3Namespace) runOperation(ctx context.Context, id string) (OperationRecord, error) {
	return n.store.runOperationV3(ctx, id)
}

func (n *treeV3Namespace) movePaths(ctx context.Context, paths []string, destination string) ([]FileRecord, error) {
	return n.store.movePathsV3(ctx, paths, destination)
}

func (n *treeV3Namespace) renamePath(ctx context.Context, from, to string) error {
	return n.store.renamePathV3(ctx, from, to)
}

func (n *treeV3Namespace) trashPaths(ctx context.Context, items []TrashPath) ([]FileRecord, error) {
	return n.store.trashPathsV3(ctx, items)
}

func (n *treeV3Namespace) listTrash(ctx context.Context) ([]FileRecord, error) {
	return n.store.listTrashV3(ctx)
}

func (n *treeV3Namespace) listTrashRecords(ctx context.Context, ids []string) ([]FileRecord, error) {
	return n.store.listTrashRecordsV3(ctx, ids)
}

func (n *treeV3Namespace) restoreTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	return n.store.restoreTrashV3(ctx, ids)
}

func (n *treeV3Namespace) claimTrash(ctx context.Context, ids []string) ([]FileRecord, error) {
	return n.store.claimTrashV3(ctx, ids)
}

func (n *treeV3Namespace) deleteTrash(ctx context.Context, ids []string) (int64, error) {
	return n.store.deleteTrashV3(ctx, ids)
}

var _ treeNamespace = (*treeV3Namespace)(nil)
var _ treeNamespace = (*treeV4Namespace)(nil)
