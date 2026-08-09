import {
  AlertCircle,
  DatabaseZap,
  FolderInput,
  Grid2X2,
  List,
  LoaderCircle,
  RefreshCcw,
  RotateCcw,
  Search,
  Trash2,
  Upload,
} from 'lucide-react';
import { lazy, Suspense, useEffect, useMemo } from 'react';
import {
  useLocation,
  useNavigate,
  useParams,
  type MetaFunction,
} from 'react-router';

import { Alert } from '../components/ui/alert';
import { ActivityDock } from '../components/activity-dock';
import {
  Breadcrumbs,
  FolderMetric,
  TrashEmptyState,
} from '../components/files/file-browser-chrome';
import {
  EmptyState,
  LoadingTable,
} from '../components/files/file-browser-states';
import { FileTable } from '../components/files/file-entry-views';
import { FileInspector } from '../components/files/file-inspector';
import { UploadActivity } from '../components/files/upload-activity';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '../components/ui/alert-dialog';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import {
  ConfirmPermanentDelete,
  ConfirmTrashDialog,
  MoveDialog,
  RenameDialog,
} from '../components/file-actions';
import {
  FILE_PAGE_SIZE,
  useFilesController,
} from '../features/files/presentation/use-files-controller';
import {
  createThumbnail,
  deleteThumbnails,
  filesHttpGateway,
  getDownloadUrl,
  getPreviewUrl,
  getThumbnailUrl,
} from '../features/files/infrastructure/files-http-gateway';
import { createShareDraft } from '../features/share/infrastructure/share-http-gateway';
import { removeArchiveTemporaryFiles } from '../lib/archive-temporary-storage';
import {
  fileBrowserPath,
  DRIFT_ROUTE,
  logicalPathFromRoute,
  TRASH_ROUTE,
} from '../lib/file-route';
import { formatBytes } from '../lib/format';
import { cn } from '../lib/utils';

const loadUploadDialog = () =>
  import('../features/upload/presentation/upload-dialog');
const UploadDialog = lazy(() =>
  loadUploadDialog().then((module) => ({ default: module.UploadDialog }))
);

const filesControllerDependencies = {
  ...filesHttpGateway,
  createShareDraft,
  createThumbnail,
  deleteThumbnails,
  getPreviewUrl,
  removeArchiveTemporaryFiles,
};

const filesPresentationDependencies = {
  loadTree: filesHttpGateway.getTree,
  getDownloadUrl,
  getPreviewUrl,
  getThumbnailUrl,
};

export const meta: MetaFunction = () => [
  { title: 'vfs-link browser' },
  {
    name: 'description',
    content: 'File browser for vfs-link FTP storage.',
  },
];

export default function FileBrowserRoute() {
  const navigate = useNavigate();
  const location = useLocation();
  const { '*': routePath } = useParams();
  const currentPath = useMemo(
    () => logicalPathFromRoute(routePath),
    [routePath]
  );
  const normalizedRoutePath = location.pathname.replace(/\/+$/, '') || '/';
  const view = normalizedRoutePath === TRASH_ROUTE ? 'trash' : 'files';
  useEffect(() => {
    const canonicalPath =
      view === 'trash' ? TRASH_ROUTE : fileBrowserPath(currentPath);
    if (location.pathname !== canonicalPath) {
      navigate(canonicalPath, { replace: true });
    }
  }, [currentPath, location.pathname, navigate, view]);

  const controller = useFilesController({
    currentPath,
    dependencies: filesControllerDependencies,
    navigate,
    view,
  });
  const {
    listing: {
      changeFileViewMode,
      currentEntries,
      currentPagination,
      fileViewMode,
      folderSummary,
      openFolder,
      query,
      refresh,
      setPageOffset,
      setQuery,
      state,
      totalVisibleBytes,
    },
    trash: {
      actionTrashIds,
      loadTrash,
      restoreEntry,
      selectedTrashIds,
      setActionTrashIds,
      setShowEmptyConfirm,
      setShowPermanentConfirm,
      showEmptyConfirm,
      showPermanentConfirm,
      trashEntries,
      trashLoading,
    },
    selection: {
      entryKey,
      existingNames,
      selectFile,
      selectedFile,
      selection,
      setSelectedFile,
    },
    operations: {
      actionError,
      actionPaths,
      activeOperation,
      beginMove,
      beginRename,
      beginTrash,
      renameEntry,
      runEmptyTrash,
      runMove,
      runPermanentDelete,
      runRename,
      runRestore,
      runTrash,
      setRenameEntry,
      setShowMove,
      setShowTrashConfirm,
      showMove,
      showTrashConfirm,
    },
    uploads: {
      addArchiveBatches,
      setShowCancelUploadsConfirm,
      setShowUpload,
      setUploadDockExpanded,
      showCancelUploadsConfirm,
      showUpload,
      uploadDockExpanded,
      uploadQueue,
    },
    sharing: { shareError, shareFile, sharingPath },
    hasActivityDock,
  } = controller;

  return (
    <main className="min-h-screen bg-background text-foreground lg:h-screen lg:min-h-0 lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1440px] flex-col gap-3 px-4 py-4 sm:px-6 lg:h-full lg:min-h-0 lg:px-8">
        <header className="flex flex-col gap-3 border-b border-border pb-3 xl:flex-row xl:items-center xl:justify-between">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <h1 className="mr-1 text-2xl font-semibold tracking-normal sm:text-3xl">
              vfs-link file browser
            </h1>
          </div>
          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-end xl:shrink-0">
            {view === 'files' && (
              <div className="relative w-full md:w-[260px]">
                <Search
                  aria-hidden="true"
                  className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
                />
                <Input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder="Search current folder"
                  className="h-9 pl-8"
                />
              </div>
            )}
            {view === 'files' && (
              <FolderMetric
                value={`${folderSummary?.files ?? 0} files`}
                detail={formatBytes(folderSummary?.bytes ?? 0)}
              />
            )}
            {view === 'files' && (
              <Button
                variant="outline"
                onClick={() => navigate(DRIFT_ROUTE)}
                className="h-9 w-full px-3 md:w-auto"
              >
                <DatabaseZap aria-hidden="true" className="h-4 w-4" />
                Drift
              </Button>
            )}
            {view === 'files' && (
              <Button
                variant="outline"
                disabled={!uploadQueue.isUploadLeader}
                onClick={() => setShowUpload((visible) => !visible)}
                onFocus={() => void loadUploadDialog()}
                onPointerEnter={() => void loadUploadDialog()}
                className="h-9 w-full px-3 md:w-auto"
                aria-expanded={showUpload}
                title={
                  uploadQueue.isUploadLeader
                    ? '上傳'
                    : '另一個 VFS Link 分頁正在管理上傳'
                }
              >
                <Upload aria-hidden="true" className="h-4 w-4" />
                Upload
              </Button>
            )}
            <Button
              variant={view === 'trash' ? 'secondary' : 'outline'}
              onClick={() =>
                navigate(
                  view === 'files' ? TRASH_ROUTE : fileBrowserPath(currentPath)
                )
              }
              className="h-9 w-full px-3 md:w-auto"
            >
              <Trash2 aria-hidden="true" className="h-4 w-4" />
              {view === 'files' ? 'Trash' : 'Back to files'}
            </Button>
            <Button
              variant="outline"
              onClick={() => (view === 'files' ? refresh() : void loadTrash())}
              disabled={view === 'files' ? state.loading : trashLoading}
              title="重新整理"
              className="h-9 w-full px-3 md:w-auto"
            >
              <RefreshCcw aria-hidden="true" className="h-4 w-4" />
              Refresh
            </Button>
          </div>
        </header>

        <section className="flex min-w-0 flex-col gap-4 lg:min-h-0 lg:flex-1">
          <div className="flex items-center gap-3 overflow-hidden rounded-lg border border-border bg-white p-4">
            <div className="min-w-0 flex-1">
              {view === 'files' ? (
                <Breadcrumbs
                  entries={state.files?.breadcrumbs ?? []}
                  currentPath={currentPath}
                  onSelectPath={openFolder}
                />
              ) : (
                <div className="flex items-center gap-2 font-semibold">
                  <Trash2 className="h-4 w-4" />
                  Trash <Badge variant="secondary">{trashEntries.length}</Badge>
                </div>
              )}
            </div>
            {view === 'files' && (
              <div
                className="flex shrink-0 items-center rounded-md border border-border p-0.5"
                role="group"
                aria-label="File display mode"
              >
                <Button
                  type="button"
                  variant={fileViewMode === 'list' ? 'secondary' : 'ghost'}
                  size="icon"
                  className="h-8 w-8"
                  aria-label="List view"
                  aria-pressed={fileViewMode === 'list'}
                  title="List view"
                  onClick={() => changeFileViewMode('list')}
                >
                  <List aria-hidden="true" className="h-4 w-4" />
                </Button>
                <Button
                  type="button"
                  variant={fileViewMode === 'grid' ? 'secondary' : 'ghost'}
                  size="icon"
                  className="h-8 w-8"
                  aria-label="Grid view"
                  aria-pressed={fileViewMode === 'grid'}
                  title="Grid view"
                  onClick={() => changeFileViewMode('grid')}
                >
                  <Grid2X2 aria-hidden="true" className="h-4 w-4" />
                </Button>
              </div>
            )}
          </div>

          {view === 'trash' &&
            trashEntries.length > 0 &&
            selection.selected.size === 0 && (
              <div className="flex justify-end">
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => setShowEmptyConfirm(true)}
                >
                  <Trash2 className="h-4 w-4" />
                  Empty trash
                </Button>
              </div>
            )}

          <section
            className={cn(
              'grid gap-4 lg:min-h-0 lg:flex-1',
              view === 'files' &&
                'xl:grid-cols-[minmax(0,1fr)_360px] 2xl:grid-cols-[minmax(0,1fr)_420px]'
            )}
          >
            <div className="min-h-0 overflow-hidden rounded-lg border border-border bg-white lg:flex-1">
              {(
                view === 'files' ? state.loading && !state.files : trashLoading
              ) ? (
                <LoadingTable />
              ) : currentEntries.length === 0 ? (
                view === 'files' ? (
                  <EmptyState query={query} />
                ) : (
                  <TrashEmptyState />
                )
              ) : (
                <FileTable
                  dependencies={filesPresentationDependencies}
                  entries={currentEntries}
                  viewMode={view === 'files' ? fileViewMode : 'list'}
                  pagination={view === 'files' ? currentPagination : undefined}
                  pageSize={FILE_PAGE_SIZE}
                  visibleBytes={
                    view === 'files'
                      ? totalVisibleBytes
                      : currentEntries.reduce(
                          (sum, entry) => sum + entry.size,
                          0
                        )
                  }
                  sharingPath={sharingPath}
                  selectedPaths={selection.selected}
                  trashView={view === 'trash'}
                  entryKey={entryKey}
                  onPageChange={setPageOffset}
                  onOpenFolder={openFolder}
                  onSelectFile={selectFile}
                  onSelect={(entry, options) => {
                    selection.select(entryKey(entry), options);
                    setSelectedFile(
                      entry.kind === 'file' && !options.toggle && !options.range
                        ? entry
                        : undefined
                    );
                  }}
                  onMove={(entry) => beginMove([entry.path])}
                  onRename={beginRename}
                  onTrash={(entry) => beginTrash([entry.path])}
                  onRestore={(entry) => {
                    if (entry.trashId) void restoreEntry(entry.trashId);
                  }}
                  onPermanentDelete={(entry) => {
                    if (entry.trashId) {
                      setActionTrashIds([entry.trashId]);
                      setShowPermanentConfirm(true);
                    }
                  }}
                  onShareFile={shareFile}
                />
              )}
            </div>
            {view === 'files' && (
              <FileInspector
                dependencies={filesPresentationDependencies}
                file={selectedFile}
                sharingPath={sharingPath}
                onClear={() => setSelectedFile(undefined)}
                onShareFile={shareFile}
                onMove={(file) => beginMove([file.path])}
                onRename={beginRename}
                onTrash={(file) => beginTrash([file.path])}
              />
            )}
          </section>
        </section>
        <ActivityDock
          visible={hasActivityDock}
          className={selectedFile ? 'hidden xl:flex' : undefined}
        >
          <div className="divide-y divide-border">
            {uploadQueue.items.length > 0 && (
              <UploadActivity
                queue={uploadQueue}
                expanded={uploadDockExpanded}
                onExpandedChange={setUploadDockExpanded}
                onRequestCancelAll={() => setShowCancelUploadsConfirm(true)}
              />
            )}
            {!uploadQueue.isUploadLeader && (
              <Alert className="rounded-none border-0 bg-amber-50 text-amber-950">
                <div className="flex items-start gap-3">
                  <AlertCircle className="mt-0.5 h-5 w-5 shrink-0" />
                  <div>
                    <p className="font-semibold">另一個分頁正在管理上傳</p>
                    <p className="text-sm text-amber-900/80">
                      為避免重複送出
                      chunk，本分頁暫停上傳控制；關閉主要分頁後，本頁會接手並重新載入
                      queue。
                    </p>
                  </div>
                </div>
              </Alert>
            )}
            {state.error && (
              <Alert className="rounded-none border-0 text-destructive">
                <div className="flex items-start gap-3">
                  <AlertCircle
                    aria-hidden="true"
                    className="mt-0.5 h-5 w-5 shrink-0"
                  />
                  <div className="grid gap-1">
                    <p className="font-semibold">API unavailable</p>
                    <p className="text-sm text-foreground">{state.error}</p>
                  </div>
                </div>
              </Alert>
            )}
            {shareError && (
              <Alert className="rounded-none border-0 text-destructive">
                <div className="flex items-start gap-3">
                  <AlertCircle
                    aria-hidden="true"
                    className="mt-0.5 h-5 w-5 shrink-0"
                  />
                  <div className="grid gap-1">
                    <p className="font-semibold">Share unavailable</p>
                    <p className="text-sm text-foreground">{shareError}</p>
                  </div>
                </div>
              </Alert>
            )}
            {actionError && (
              <Alert className="rounded-none border-0 text-destructive">
                <div className="flex items-start gap-3">
                  <AlertCircle className="mt-0.5 h-5 w-5 shrink-0" />
                  <div className="grid gap-1">
                    <p className="font-semibold">Action unavailable</p>
                    <p className="text-sm text-foreground">{actionError}</p>
                  </div>
                </div>
              </Alert>
            )}
            {activeOperation && (
              <Alert className="rounded-none border-0 bg-primary/5 text-foreground">
                <div className="flex items-start gap-3">
                  <LoaderCircle
                    aria-hidden="true"
                    className="mt-0.5 h-5 w-5 shrink-0 animate-spin text-primary"
                  />
                  <div className="grid gap-1">
                    <p className="font-semibold">
                      {activeOperation.type === 'move'
                        ? 'Move in progress'
                        : activeOperation.type === 'rename'
                        ? 'Rename in progress'
                        : activeOperation.type === 'trash'
                        ? 'Moving to trash'
                        : activeOperation.type === 'restore'
                        ? 'Restore in progress'
                        : 'Permanent deletion in progress'}
                    </p>
                    <p className="text-sm text-muted-foreground">
                      {activeOperation.total > 0
                        ? `${activeOperation.progress.toLocaleString()} of ${activeOperation.total.toLocaleString()} metadata records`
                        : 'Preparing metadata operation…'}
                    </p>
                  </div>
                </div>
              </Alert>
            )}
            {selection.selected.size > 0 && (
              <div
                className="flex flex-wrap items-center gap-2 p-3"
                role="toolbar"
                aria-label="Selected item actions"
              >
                <Badge variant="secondary">
                  {selection.selected.size} selected
                </Badge>
                {view === 'files' ? (
                  <>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => beginMove(selection.selectedPaths)}
                    >
                      <FolderInput className="h-4 w-4" />
                      Move
                    </Button>
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() => beginTrash(selection.selectedPaths)}
                    >
                      <Trash2 className="h-4 w-4" />
                      Move to trash
                    </Button>
                  </>
                ) : (
                  <>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => void runRestore()}
                    >
                      <RotateCcw className="h-4 w-4" />
                      Restore
                    </Button>
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() => {
                        setActionTrashIds(selectedTrashIds);
                        setShowPermanentConfirm(true);
                      }}
                    >
                      <Trash2 className="h-4 w-4" />
                      Delete permanently
                    </Button>
                  </>
                )}
                <Button size="sm" variant="ghost" onClick={selection.clear}>
                  Clear
                </Button>
              </div>
            )}
          </div>
        </ActivityDock>
        {showUpload && (
          <Suspense fallback={null}>
            <UploadDialog
              dependencies={{ removeArchiveTemporaryFiles }}
              currentPath={currentPath}
              onAddFiles={(candidates) =>
                uploadQueue.add(candidates, currentPath, existingNames)
              }
              onAddArchives={addArchiveBatches}
              open
              onOpenChange={setShowUpload}
            />
          </Suspense>
        )}
        <MoveDialog
          dependencies={filesPresentationDependencies}
          open={showMove}
          count={actionPaths.length}
          initialPath={currentPath}
          onOpenChange={setShowMove}
          onMove={runMove}
        />
        <RenameDialog
          open={Boolean(renameEntry)}
          entry={renameEntry}
          onOpenChange={(open) => {
            if (!open) setRenameEntry(undefined);
          }}
          onRename={runRename}
        />
        <ConfirmTrashDialog
          open={showTrashConfirm}
          count={actionPaths.length}
          onOpenChange={setShowTrashConfirm}
          onConfirm={runTrash}
        />
        <ConfirmPermanentDelete
          open={showPermanentConfirm}
          title="Delete permanently?"
          description={`${
            actionTrashIds.length || selection.selected.size
          } selected item${
            (actionTrashIds.length || selection.selected.size) === 1 ? '' : 's'
          } will be removed from storage. This cannot be undone.`}
          onOpenChange={(open) => {
            setShowPermanentConfirm(open);
            if (!open) setActionTrashIds([]);
          }}
          onConfirm={runPermanentDelete}
        />
        <ConfirmPermanentDelete
          open={showEmptyConfirm}
          title="Empty trash?"
          description={`All ${trashEntries.length} trashed items will be removed from storage. This cannot be undone.`}
          action="Empty trash"
          onOpenChange={setShowEmptyConfirm}
          onConfirm={runEmptyTrash}
        />
        <AlertDialog
          open={showCancelUploadsConfirm}
          onOpenChange={setShowCancelUploadsConfirm}
        >
          <AlertDialogContent>
            <AlertDialogTitle className="text-lg font-semibold">
              Cancel all uploads?
            </AlertDialogTitle>
            <AlertDialogDescription className="text-sm text-muted-foreground">
              {uploadQueue.summary.queued +
                uploadQueue.summary.uploading +
                uploadQueue.summary.retrying +
                uploadQueue.summary.paused}{' '}
              active or paused file
              {uploadQueue.summary.queued +
                uploadQueue.summary.uploading +
                uploadQueue.summary.retrying +
                uploadQueue.summary.paused ===
              1
                ? ''
                : 's'}{' '}
              will be stopped and removed from this process list.
            </AlertDialogDescription>
            <div className="flex justify-end gap-2">
              <AlertDialogCancel asChild>
                <Button variant="outline">Keep uploads</Button>
              </AlertDialogCancel>
              <AlertDialogAction asChild>
                <Button
                  variant="destructive"
                  onClick={() => uploadQueue.cancelAll()}
                >
                  Cancel uploads
                </Button>
              </AlertDialogAction>
            </div>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </main>
  );
}
