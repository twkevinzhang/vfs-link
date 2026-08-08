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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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
  UploadDialog,
  type PreparedArchiveBatch,
} from '../components/upload-panel';
import {
  ConfirmPermanentDelete,
  ConfirmTrashDialog,
  MoveDialog,
  RenameDialog,
} from '../components/file-actions';
import { useFileSelection } from '../hooks/use-file-selection';
import {
  useBackgroundUploadQueue,
  type UploadQueueItem,
} from '../hooks/use-upload-queue';
import { appPath } from '../lib/base-path';
import {
  fileBrowserPath,
  DRIFT_ROUTE,
  logicalPathFromRoute,
  TRASH_ROUTE,
} from '../lib/file-route';
import {
  type FileViewMode,
  readFileViewMode,
  writeFileViewMode,
} from '../lib/file-view-mode';
import {
  createShareDraft,
  createThumbnail,
  deleteTrash,
  deleteThumbnails,
  emptyTrash,
  getFiles,
  getFileOperation,
  getPreviewUrl,
  getStatus,
  getTrash,
  moveFiles,
  moveFilesToTrash,
  renameFile,
  restoreTrash,
} from '../lib/api';
import { removeArchiveTemporaryFiles } from '../lib/archive-compression';
import { formatBytes, normalizePath } from '../lib/format';
import { cn } from '../lib/utils';
import {
  FileEntry,
  FileOperationResponse,
  FilesResponse,
  StatusResponse,
  TrashEntry,
} from '../types/files';

export const meta: MetaFunction = () => [
  { title: 'vfs-link browser' },
  {
    name: 'description',
    content: 'File browser for vfs-link FTP storage.',
  },
];

type LoadState = {
  status?: StatusResponse;
  files?: FilesResponse;
  loading: boolean;
  error?: string;
};

const FILE_PAGE_SIZE = 100;
const SEARCH_DEBOUNCE_MS = 250;
const UPLOAD_REFRESH_DEBOUNCE_MS = 300;
const EMPTY_FILE_ENTRIES: FileEntry[] = [];

type PendingArchiveBatch = PreparedArchiveBatch & {
  paths: Set<string>;
  completed: Set<string>;
  processing: boolean;
};

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
  const [query, setQuery] = useState('');
  const [fileQuery, setFileQuery] = useState('');
  const [fileViewMode, setFileViewMode] = useState<FileViewMode>(() =>
    readFileViewMode(
      typeof window === 'undefined' ? undefined : window.localStorage
    )
  );
  const [pageOffset, setPageOffset] = useState(0);
  const [state, setState] = useState<LoadState>({ loading: true });
  const [shareError, setShareError] = useState<string>();
  const [sharingPath, setSharingPath] = useState<string>();
  const [selectedFile, setSelectedFile] = useState<FileEntry>();
  const [showUpload, setShowUpload] = useState(false);
  const [trashEntries, setTrashEntries] = useState<TrashEntry[]>([]);
  const [trashLoading, setTrashLoading] = useState(false);
  const [actionError, setActionError] = useState<string>();
  const [activeOperation, setActiveOperation] =
    useState<FileOperationResponse>();
  const [actionPaths, setActionPaths] = useState<string[]>([]);
  const [actionTrashIds, setActionTrashIds] = useState<string[]>([]);
  const [showMove, setShowMove] = useState(false);
  const [renameEntry, setRenameEntry] = useState<FileEntry>();
  const [showTrashConfirm, setShowTrashConfirm] = useState(false);
  const [showPermanentConfirm, setShowPermanentConfirm] = useState(false);
  const [showEmptyConfirm, setShowEmptyConfirm] = useState(false);
  const [uploadDockExpanded, setUploadDockExpanded] = useState(true);
  const [showCancelUploadsConfirm, setShowCancelUploadsConfirm] =
    useState(false);
  const filesRequestRef = useRef(0);
  const uploadRefreshTimerRef = useRef<number | undefined>(undefined);
  const completedUploadDestinationsRef = useRef(new Set<string>());
  const handledCompletedUploadsRef = useRef(new Set<string>());
  const archiveBatchesRef = useRef(new Map<string, PendingArchiveBatch>());
  const uploadRouteRef = useRef({
    currentPath,
    fileQuery,
    pageOffset,
    view,
  });
  uploadRouteRef.current = { currentPath, fileQuery, pageOffset, view };

  useEffect(() => {
    const canonicalPath =
      view === 'trash' ? TRASH_ROUTE : fileBrowserPath(currentPath);
    if (location.pathname !== canonicalPath) {
      navigate(canonicalPath, { replace: true });
    }
  }, [currentPath, location.pathname, navigate, view]);

  const loadStatus = useCallback(async () => {
    try {
      const status = await getStatus();
      setState((previous) => ({ ...previous, status }));
    } catch (error) {
      setState((previous) => ({
        ...previous,
        error: error instanceof Error ? error.message : 'Unable to load status',
      }));
    }
  }, []);

  const loadFiles = useCallback(
    async (path: string, searchQuery: string, offset: number) => {
      const requestId = filesRequestRef.current + 1;
      filesRequestRef.current = requestId;
      setState((previous) => ({
        ...previous,
        loading: true,
        error: undefined,
      }));
      try {
        const files = await getFiles(path, {
          query: searchQuery,
          limit: FILE_PAGE_SIZE,
          offset,
        });
        if (filesRequestRef.current !== requestId) {
          return;
        }
        setState((previous) => ({ ...previous, files, loading: false }));
      } catch (error) {
        if (filesRequestRef.current !== requestId) {
          return;
        }
        setState((previous) => ({
          ...previous,
          loading: false,
          error:
            error instanceof Error ? error.message : 'Unable to load files',
        }));
      }
    },
    []
  );

  const handleUploadComplete = useCallback(
    (item: UploadQueueItem) => {
      completedUploadDestinationsRef.current.add(item.destinationPath);
      if (uploadRefreshTimerRef.current !== undefined) {
        window.clearTimeout(uploadRefreshTimerRef.current);
      }
      uploadRefreshTimerRef.current = window.setTimeout(() => {
        uploadRefreshTimerRef.current = undefined;
        const completedDestinations = new Set(
          completedUploadDestinationsRef.current
        );
        completedUploadDestinationsRef.current.clear();
        const activeRoute = uploadRouteRef.current;
        void loadStatus();
        if (
          activeRoute.view === 'files' &&
          completedDestinations.has(activeRoute.currentPath)
        ) {
          setPageOffset(0);
          void loadFiles(activeRoute.currentPath, activeRoute.fileQuery, 0);
        }
      }, UPLOAD_REFRESH_DEBOUNCE_MS);
    },
    [loadFiles, loadStatus]
  );

  useEffect(
    () => () => {
      if (uploadRefreshTimerRef.current !== undefined) {
        window.clearTimeout(uploadRefreshTimerRef.current);
      }
    },
    []
  );

  const uploadQueue = useBackgroundUploadQueue();
  const failedUploadKeysRef = useRef(new Set<string>());

  useEffect(() => {
    const failedKeys = new Set(
      uploadQueue.items
        .filter(
          (item) => item.state === 'failed' || item.state === 'needs-decision'
        )
        .map((item) => item.key)
    );
    if ([...failedKeys].some((key) => !failedUploadKeysRef.current.has(key))) {
      setUploadDockExpanded(true);
    }
    failedUploadKeysRef.current = failedKeys;
  }, [uploadQueue.items]);

  useEffect(() => {
    const activeKeys = new Set(uploadQueue.items.map((item) => item.key));
    for (const key of handledCompletedUploadsRef.current) {
      if (!activeKeys.has(key)) handledCompletedUploadsRef.current.delete(key);
    }
    for (const item of uploadQueue.items) {
      if (
        item.state === 'complete' &&
        !handledCompletedUploadsRef.current.has(item.key)
      ) {
        handledCompletedUploadsRef.current.add(item.key);
        handleUploadComplete(item);
        if (item.archiveGroupId) {
          const batch = archiveBatchesRef.current.get(item.archiveGroupId);
          if (batch) {
            batch.completed.add(item.logicPath);
            const complete = [...batch.paths].every((path) =>
              batch.completed.has(path)
            );
            if (complete && !batch.processing) {
              batch.processing = true;
              void (async () => {
                try {
                  if (batch.thumbnail) {
                    await createThumbnail({
                      paths: [...batch.paths],
                      ...batch.thumbnail,
                    });
                  } else {
                    await deleteThumbnails([...batch.paths]);
                  }
                } catch (error) {
                  setActionError(
                    error instanceof Error
                      ? `縮圖儲存失敗：${error.message}`
                      : '縮圖儲存失敗。'
                  );
                } finally {
                  await removeArchiveTemporaryFiles(batch.temporaryNames);
                  archiveBatchesRef.current.delete(batch.id);
                  const route = uploadRouteRef.current;
                  if (route.view === 'files') {
                    void loadFiles(route.currentPath, route.fileQuery, 0);
                  }
                }
              })();
            }
          }
        }
      }
    }
  }, [handleUploadComplete, loadFiles, uploadQueue.items]);

  const refresh = useCallback(() => {
    const nextOffset = 0;
    setPageOffset(nextOffset);
    void Promise.all([
      loadStatus(),
      loadFiles(currentPath, fileQuery, nextOffset),
    ]);
  }, [currentPath, fileQuery, loadFiles, loadStatus]);

  const changeFileViewMode = useCallback((mode: FileViewMode) => {
    setFileViewMode(mode);
    writeFileViewMode(
      typeof window === 'undefined' ? undefined : window.localStorage,
      mode
    );
  }, []);

  const loadTrash = useCallback(async () => {
    setTrashLoading(true);
    setActionError(undefined);
    try {
      const response = await getTrash();
      setTrashEntries(response.entries);
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : 'Unable to load trash'
      );
    } finally {
      setTrashLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadStatus();
  }, [loadStatus]);

  useEffect(() => {
    const timeout = window.setTimeout(() => {
      setPageOffset(0);
      setFileQuery(query.trim());
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timeout);
  }, [query]);

  useEffect(() => {
    void loadFiles(currentPath, fileQuery, pageOffset);
  }, [currentPath, fileQuery, pageOffset, loadFiles]);

  const visibleEntries = state.files?.entries ?? EMPTY_FILE_ENTRIES;
  const currentEntries: FileEntry[] =
    view === 'trash' ? trashEntries : visibleEntries;
  const entryKey = useCallback(
    (entry: FileEntry) =>
      view === 'trash' ? entry.trashId ?? entry.path : entry.path,
    [view]
  );
  const selectionPaths = useMemo(
    () => currentEntries.map(entryKey),
    [currentEntries, entryKey]
  );
  const selection = useFileSelection(selectionPaths);
  const clearSelection = selection.clear;
  const selectedEntries = currentEntries.filter((entry) =>
    selection.selected.has(entryKey(entry))
  );
  const selectedTrashIds = selectedEntries
    .map((entry) => entry.trashId)
    .filter((id): id is string => Boolean(id));
  const existingNames = useMemo(
    () => new Set(visibleEntries.map((entry) => entry.name)),
    [visibleEntries]
  );
  const addArchiveBatches = useCallback(
    (batches: PreparedArchiveBatch[]) => {
      const allCandidates = batches.flatMap((batch) => {
        const paths = new Set(
          batch.candidates.map((candidate) =>
            normalizePath(
              [currentPath, candidate.relativePath].filter(Boolean).join('/')
            )
          )
        );
        archiveBatchesRef.current.set(batch.id, {
          ...batch,
          paths,
          completed: new Set(),
          processing: false,
        });
        return batch.candidates;
      });
      uploadQueue.add(allCandidates, currentPath, existingNames);
    },
    [currentPath, existingNames, uploadQueue]
  );
  const currentPagination = state.files?.pagination;
  const totalVisibleBytes = state.files?.visibleBytes ?? 0;
  const folderSummary = state.files?.folderSummary;

  const selectFile = useCallback((entry: FileEntry) => {
    setSelectedFile(entry);
    setShareError(undefined);
  }, []);

  const openFolder = useCallback(
    (path: string) => {
      setSelectedFile(undefined);
      setQuery('');
      setFileQuery('');
      setPageOffset(0);
      navigate(fileBrowserPath(path));
    },
    [navigate]
  );

  useEffect(() => {
    clearSelection();
    setSelectedFile(undefined);
    if (view === 'trash') void loadTrash();
  }, [clearSelection, currentPath, loadTrash, view]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const input = target?.closest('input');
      const isTextInput =
        input instanceof HTMLInputElement &&
        !['checkbox', 'radio', 'button'].includes(input.type);
      if (
        isTextInput ||
        target?.closest(
          'textarea, select, [contenteditable="true"], [role="dialog"], [role="menu"]'
        )
      )
        return;
      const modifier = event.metaKey || event.ctrlKey;
      if (modifier && event.key.toLowerCase() === 'a') {
        event.preventDefault();
        selection.selectAll();
      } else if (event.key === 'Escape') {
        selection.clear();
        setSelectedFile(undefined);
      } else if (
        event.key === 'Enter' &&
        selectedEntries.length === 1 &&
        view === 'files'
      ) {
        event.preventDefault();
        const entry = selectedEntries[0];
        if (entry.kind === 'directory') openFolder(entry.path);
        else
          window.open(
            getPreviewUrl(entry.path),
            '_blank',
            'noopener,noreferrer'
          );
      } else if (
        view === 'files' &&
        selectedEntries.length > 0 &&
        (event.key === 'Delete' || (event.metaKey && event.key === 'Backspace'))
      ) {
        event.preventDefault();
        setActionPaths(selectedEntries.map((entry) => entry.path));
        setShowTrashConfirm(true);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [openFolder, selectedEntries, selection, view]);

  const beginMove = (paths: string[]) => {
    setActionPaths(paths);
    setShowMove(true);
  };
  const beginTrash = (paths: string[]) => {
    setActionPaths(paths);
    setShowTrashConfirm(true);
  };
  const beginRename = (entry: FileEntry) => {
    setActionError(undefined);
    setRenameEntry(entry);
  };
  const runRename = async (name: string) => {
    const entry = renameEntry;
    if (!entry) return;

    try {
      const result = await renameFile(entry.path, name);
      setRenameEntry(undefined);
      selection.clear();
      setSelectedFile(undefined);
      if ('operationId' in result) {
        setActiveOperation(result);
        void watchOperation(result.operationId);
        return;
      }
      refresh();
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'Unable to rename item';
      setActionError(message);
      throw error instanceof Error ? error : new Error(message);
    }
  };
  const runMove = async (destination: string) => {
    try {
      const result = await moveFiles(actionPaths, destination);
      setShowMove(false);
      selection.clear();
      if ('operationId' in result) {
        setActiveOperation(result);
        void watchOperation(result.operationId);
        return;
      }
      refresh();
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : 'Unable to move items'
      );
    }
  };

  const watchOperation = async (id: string) => {
    try {
      for (;;) {
        const operation = await getFileOperation(id);
        setActiveOperation(operation);
        if (operation.status === 'completed') {
          setActiveOperation(undefined);
          selection.clear();
          setSelectedFile(undefined);
          refresh();
          void loadTrash();
          void loadStatus();
          return;
        }
        if (operation.status === 'failed') {
          setActiveOperation(undefined);
          setActionError(operation.error || 'Background operation failed');
          return;
        }
        await new Promise((resolve) => window.setTimeout(resolve, 1500));
      }
    } catch (error) {
      setActiveOperation(undefined);
      setActionError(
        error instanceof Error
          ? error.message
          : 'Unable to monitor background operation'
      );
    }
  };
  const runTrash = async () => {
    try {
      const result = await moveFilesToTrash(actionPaths);
      setShowTrashConfirm(false);
      selection.clear();
      setSelectedFile(undefined);
      if ('operationId' in result) {
        setActiveOperation(result);
        void watchOperation(result.operationId);
        return;
      }
      refresh();
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : 'Unable to move items to trash'
      );
    }
  };
  const runRestore = async () => {
    const trashIds = selectedTrashIds;
    try {
      const result = await restoreTrash(trashIds);
      selection.clear();
      if ('operationId' in result) {
        setActiveOperation(result);
        void watchOperation(result.operationId);
        return;
      }
      await Promise.all([loadTrash(), loadStatus()]);
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : 'Unable to restore items'
      );
    }
  };
  const runPermanentDelete = async () => {
    const trashIds =
      actionTrashIds.length > 0 ? actionTrashIds : selectedTrashIds;
    try {
      const result = await deleteTrash(trashIds);
      setShowPermanentConfirm(false);
      selection.clear();
      if ('operationId' in result) {
        setActiveOperation(result);
        void watchOperation(result.operationId);
        return;
      }
      await Promise.all([loadTrash(), loadStatus()]);
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : 'Unable to delete items'
      );
    }
  };
  const runEmptyTrash = async () => {
    try {
      const result = await emptyTrash();
      setShowEmptyConfirm(false);
      selection.clear();
      if ('operationId' in result) {
        setActiveOperation(result);
        void watchOperation(result.operationId);
        return;
      }
      await Promise.all([loadTrash(), loadStatus()]);
    } catch (error) {
      setActionError(
        error instanceof Error ? error.message : 'Unable to empty trash'
      );
    }
  };

  const shareFile = useCallback(async (path: string) => {
    setShareError(undefined);
    setSharingPath(path);
    const popup = window.open('about:blank', '_blank');
    try {
      const draft = await createShareDraft(path);
      const sharePath = `/share/${encodeURIComponent(draft.id)}`;
      if (popup) {
        popup.opener = null;
        popup.location.replace(appPath(sharePath));
      } else {
        window.location.href = appPath(sharePath);
      }
    } catch (error) {
      popup?.close();
      setShareError(
        error instanceof Error ? error.message : 'Unable to create share'
      );
    } finally {
      setSharingPath(undefined);
    }
  }, []);

  const hasActivityDock = Boolean(
    state.error ||
      shareError ||
      actionError ||
      activeOperation ||
      uploadQueue.items.length > 0 ||
      !uploadQueue.isUploadLeader ||
      selection.selected.size > 0
  );

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
                    if (entry.trashId)
                      void restoreTrash([entry.trashId])
                        .then(() => {
                          selection.clear();
                          void loadTrash();
                          void loadStatus();
                        })
                        .catch((error) =>
                          setActionError(
                            error instanceof Error
                              ? error.message
                              : 'Unable to restore item'
                          )
                        );
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
        <UploadDialog
          currentPath={currentPath}
          onAddFiles={(candidates) =>
            uploadQueue.add(candidates, currentPath, existingNames)
          }
          onAddArchives={addArchiveBatches}
          open={showUpload}
          onOpenChange={setShowUpload}
        />
        <MoveDialog
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
