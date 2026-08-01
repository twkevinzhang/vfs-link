import {
  AlertCircle,
  ChevronDown,
  ChevronUp,
  Check,
  Copy,
  Download,
  DatabaseZap,
  File,
  Folder,
  FolderInput,
  LoaderCircle,
  Pencil,
  Play,
  RefreshCcw,
  RotateCcw,
  Search,
  Share2,
  Trash2,
  Upload,
  X,
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
import { Skeleton } from '../components/ui/skeleton';
import {
  UploadDialog,
  type PreparedArchiveBatch,
} from '../components/upload-panel';
import {
  ConfirmPermanentDelete,
  ConfirmTrashDialog,
  FileActionMenu,
  MoveDialog,
  RenameDialog,
} from '../components/file-actions';
import { Checkbox } from '../components/ui/checkbox';
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
  createShareDraft,
  createThumbnail,
  deleteTrash,
  deleteThumbnails,
  emptyTrash,
  getDownloadUrl,
  getFiles,
  getFileOperation,
  getPreviewUrl,
  getStatus,
  getThumbnailUrl,
  getTrash,
  moveFiles,
  moveFilesToTrash,
  renameFile,
  restoreTrash,
} from '../lib/api';
import { removeArchiveTemporaryFiles } from '../lib/archive-compression';
import {
  formatBytes,
  formatDate,
  formatPathDisplayName,
  normalizePath,
} from '../lib/format';
import { cn } from '../lib/utils';
import {
  FileEntry,
  FileOperationResponse,
  FilesResponse,
  Pagination,
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
const TEXT_PREVIEW_LIMIT = 200_000;

type TextPreviewState =
  | { status: 'idle' | 'loading' }
  | { status: 'ready'; content: string; truncated: boolean }
  | { status: 'error'; message: string };

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
        .filter((item) => item.state === 'failed')
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

  const visibleEntries = state.files?.entries ?? [];
  const currentEntries: FileEntry[] =
    view === 'trash' ? trashEntries : visibleEntries;
  const entryKey = useCallback(
    (entry: FileEntry) =>
      view === 'trash' ? entry.trashId ?? entry.path : entry.path,
    [view]
  );
  const selection = useFileSelection(currentEntries.map(entryKey));
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
    selection.clear();
    setSelectedFile(undefined);
    if (view === 'trash') void loadTrash();
  }, [currentPath, view]);

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
                onClick={() => setShowUpload((visible) => !visible)}
                className="h-9 w-full px-3 md:w-auto"
                aria-expanded={showUpload}
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
          <div className="overflow-hidden rounded-lg border border-border bg-white p-4">
            <div className="min-w-0">
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
                  pagination={view === 'files' ? currentPagination : undefined}
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
              {uploadQueue.summary.queued + uploadQueue.summary.uploading}{' '}
              queued or uploading file
              {uploadQueue.summary.queued + uploadQueue.summary.uploading === 1
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

function UploadActivity({
  queue,
  expanded,
  onExpandedChange,
  onRequestCancelAll,
}: {
  queue: Pick<
    ReturnType<typeof useBackgroundUploadQueue>,
    'items' | 'summary' | 'retry' | 'dismiss' | 'cancel'
  >;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  onRequestCancelAll: () => void;
}) {
  const { items, summary } = queue;
  const roundedProgress = Math.round(summary.progress);
  const pendingCount = summary.queued + summary.uploading;
  const queueListId = 'background-upload-queue';
  const headline =
    summary.uploading > 0
      ? `Uploading ${summary.uploading} ${
          summary.uploading === 1 ? 'file' : 'files'
        }`
      : summary.queued > 0
      ? 'Preparing uploads'
      : summary.failed > 0
      ? 'Uploads need attention'
      : 'Uploads complete';

  return (
    <section className="grid gap-3 p-3" aria-labelledby="upload-activity-title">
      <div className="grid gap-1.5">
        <div className="flex min-w-0 items-center gap-2">
          <div className="flex min-w-0 items-center gap-2">
            {pendingCount > 0 ? (
              <LoaderCircle
                aria-hidden="true"
                className="h-4 w-4 shrink-0 animate-spin text-accent"
              />
            ) : summary.failed > 0 ? (
              <AlertCircle
                aria-hidden="true"
                className="h-4 w-4 shrink-0 text-destructive"
              />
            ) : (
              <Check
                aria-hidden="true"
                className="h-4 w-4 shrink-0 text-[#11615a]"
              />
            )}
            <p
              id="upload-activity-title"
              className="truncate text-sm font-semibold"
            >
              {headline}
            </p>
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-1">
            <p className="text-right text-xs text-muted-foreground">
              <span className="sm:hidden">
                {formatBytes(summary.uploadedBytes)} · {roundedProgress}%
              </span>
              <span className="hidden sm:inline">
                {formatBytes(summary.uploadedBytes)} of{' '}
                {formatBytes(summary.totalBytes)} · {roundedProgress}%
              </span>
            </p>
            {pendingCount > 0 && (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 px-2 text-destructive hover:text-destructive"
                onClick={onRequestCancelAll}
              >
                Cancel all
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8"
              onClick={() => onExpandedChange(!expanded)}
              aria-expanded={expanded}
              aria-controls={queueListId}
              aria-label={
                expanded ? 'Collapse upload details' : 'Expand upload details'
              }
            >
              {expanded ? (
                <ChevronUp aria-hidden="true" className="h-4 w-4" />
              ) : (
                <ChevronDown aria-hidden="true" className="h-4 w-4" />
              )}
            </Button>
          </div>
        </div>
        <UploadProgress value={roundedProgress} label="Overall upload" />
        {expanded && (
          <p className="text-xs text-muted-foreground" aria-live="polite">
            {summary.complete} complete · {summary.queued} queued
            {summary.failed > 0 ? ` · ${summary.failed} failed` : ''}
          </p>
        )}
      </div>

      {expanded && (
        <ul id={queueListId} className="grid gap-2" aria-label="Upload queue">
          {items.map((item) => (
            <UploadActivityItem
              key={item.key}
              item={item}
              onCancel={() => queue.cancel(item.key)}
              onRetry={() => queue.retry(item.key)}
              onDismiss={() => queue.dismiss(item.key)}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

function UploadActivityItem({
  item,
  onCancel,
  onRetry,
  onDismiss,
}: {
  item: UploadQueueItem;
  onCancel: () => void;
  onRetry: () => void;
  onDismiss: () => void;
}) {
  const roundedProgress = Math.round(item.progress);
  const statusLabel =
    item.state === 'queued'
      ? 'Queued'
      : item.state === 'uploading'
      ? `${roundedProgress}%`
      : item.state === 'complete'
      ? 'Complete'
      : 'Failed';

  return (
    <li className="grid gap-1.5 rounded-lg border border-border bg-muted/20 p-2.5">
      <div className="flex min-w-0 items-center gap-2">
        {item.state === 'complete' ? (
          <Check
            aria-hidden="true"
            className="h-4 w-4 shrink-0 text-[#11615a]"
          />
        ) : item.state === 'failed' ? (
          <AlertCircle
            aria-hidden="true"
            className="h-4 w-4 shrink-0 text-destructive"
          />
        ) : item.state === 'uploading' ? (
          <LoaderCircle
            aria-hidden="true"
            className="h-4 w-4 shrink-0 animate-spin text-accent"
          />
        ) : (
          <Upload aria-hidden="true" className="h-4 w-4 shrink-0 text-accent" />
        )}
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium" title={item.relativePath}>
            {item.relativePath}
          </p>
          <p
            className="truncate text-xs text-muted-foreground"
            title={`Destination: ${item.destinationPath}`}
          >
            {formatBytes(item.file.size)} · to {item.destinationPath}
          </p>
        </div>
        <span className="shrink-0 text-xs text-muted-foreground">
          {statusLabel}
        </span>
        {(item.state === 'queued' || item.state === 'uploading') && (
          <Button
            variant="ghost"
            size="sm"
            className="h-8 shrink-0 px-2 text-destructive hover:text-destructive"
            onClick={onCancel}
            aria-label={`Cancel upload ${item.relativePath}`}
          >
            Cancel
          </Button>
        )}
        {item.state === 'failed' && (
          <div className="flex shrink-0 items-center gap-1">
            <Button variant="outline" size="sm" onClick={onRetry}>
              <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
              Retry
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8"
              onClick={onDismiss}
              aria-label={`Dismiss ${item.relativePath}`}
            >
              <X className="h-4 w-4" aria-hidden="true" />
            </Button>
          </div>
        )}
      </div>
      <UploadProgress value={roundedProgress} label={item.relativePath} />
      {item.error && (
        <p className="text-xs text-destructive" role="alert">
          {item.error}
        </p>
      )}
    </li>
  );
}

function UploadProgress({ value, label }: { value: number; label: string }) {
  return (
    <div
      className="h-1.5 overflow-hidden rounded-full bg-muted"
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value}
    >
      <div
        className="h-full bg-accent transition-[width]"
        style={{ width: `${value}%` }}
      />
    </div>
  );
}

function FolderMetric({ value, detail }: { value: string; detail: string }) {
  return (
    <Badge
      variant="outline"
      className="h-9 max-w-full gap-1.5 bg-white px-2.5 py-1.5 text-foreground shadow-sm"
      title={`Folder total: ${value} (${detail}, including descendants)`}
    >
      <Folder
        aria-hidden="true"
        className="h-3.5 w-3.5 shrink-0 text-muted-foreground"
      />
      <span className="truncate text-muted-foreground">Folder total</span>
      <span className="shrink-0 font-semibold">{value}</span>
      <span className="shrink-0 font-normal text-muted-foreground">
        {detail}
      </span>
    </Badge>
  );
}

function TrashEmptyState() {
  return (
    <div className="grid min-h-72 place-items-center p-8 text-center">
      <div className="grid max-w-sm gap-2">
        <Trash2
          aria-hidden="true"
          className="mx-auto h-10 w-10 text-muted-foreground"
        />
        <h2 className="font-semibold">Trash is empty</h2>
        <p className="text-sm text-muted-foreground">
          Items moved to trash stay recoverable until you permanently delete
          them.
        </p>
      </div>
    </div>
  );
}

function Breadcrumbs({
  entries,
  currentPath,
  onSelectPath,
}: {
  entries: FileEntry[];
  currentPath: string;
  onSelectPath: (path: string) => void;
}) {
  const logicalPath = normalizePath(currentPath);
  const [copiedPath, setCopiedPath] = useState<string>();
  const copyResetTimerRef = useRef<number | undefined>(undefined);
  const currentLogicalPathRef = useRef(logicalPath);
  currentLogicalPathRef.current = logicalPath;
  const crumbs =
    entries.length > 0
      ? entries
      : [
          {
            path: '',
            name: formatPathDisplayName(''),
            kind: 'directory' as const,
            size: 0,
            updatedAt: '',
          },
        ];

  useEffect(() => {
    setCopiedPath(undefined);
    return () => {
      if (copyResetTimerRef.current !== undefined) {
        window.clearTimeout(copyResetTimerRef.current);
      }
    };
  }, [logicalPath]);

  async function handleCopyPath() {
    try {
      await navigator.clipboard.writeText(logicalPath);
      if (currentLogicalPathRef.current !== logicalPath) {
        return;
      }
      if (copyResetTimerRef.current !== undefined) {
        window.clearTimeout(copyResetTimerRef.current);
      }
      setCopiedPath(logicalPath);
      copyResetTimerRef.current = window.setTimeout(() => {
        setCopiedPath((path) => (path === logicalPath ? undefined : path));
        copyResetTimerRef.current = undefined;
      }, 1500);
    } catch {
      // Clipboard access can be unavailable outside a secure browser context.
    }
  }

  const isCopied = copiedPath === logicalPath;

  return (
    <div className="flex min-w-0 items-center gap-2 text-sm">
      <div className="min-w-0 flex-1 overflow-x-auto">
        <div className="flex min-w-max flex-nowrap items-center gap-1">
          {crumbs.map((entry, index) => {
            const path = normalizePath(entry.path);
            const label = formatPathDisplayName(path, entry.name);
            const isLast = path === logicalPath || index === crumbs.length - 1;
            return (
              <div
                key={`${entry.path}-${index}`}
                className="flex shrink-0 items-center gap-1"
              >
                <Button
                  variant={isLast ? 'secondary' : 'ghost'}
                  size="sm"
                  className="max-w-none shrink-0 whitespace-nowrap"
                  onClick={() => onSelectPath(path)}
                  title={path === '' ? label : path}
                >
                  {label}
                </Button>
                {!isLast && <span className="text-muted-foreground">/</span>}
              </div>
            );
          })}
        </div>
      </div>
      <Button
        variant="outline"
        size="icon"
        onClick={() => void handleCopyPath()}
        aria-label={isCopied ? '已複製路徑' : '複製路徑'}
        title={isCopied ? '已複製路徑' : '複製路徑'}
      >
        {isCopied ? (
          <Check aria-hidden="true" className="h-4 w-4" />
        ) : (
          <Copy aria-hidden="true" className="h-4 w-4" />
        )}
      </Button>
    </div>
  );
}

function FileTable({
  entries,
  pagination,
  visibleBytes,
  sharingPath,
  selectedPaths,
  trashView,
  entryKey,
  onPageChange,
  onOpenFolder,
  onSelectFile,
  onSelect,
  onMove,
  onRename,
  onTrash,
  onRestore,
  onPermanentDelete,
  onShareFile,
}: {
  entries: FileEntry[];
  pagination?: Pagination;
  visibleBytes: number;
  sharingPath?: string;
  selectedPaths: Set<string>;
  trashView: boolean;
  entryKey: (entry: FileEntry) => string;
  onPageChange: (offset: number) => void;
  onOpenFolder: (path: string) => void;
  onSelectFile: (entry: FileEntry) => void;
  onSelect: (
    entry: FileEntry,
    options: { toggle?: boolean; range?: boolean }
  ) => void;
  onMove: (entry: FileEntry) => void;
  onRename: (entry: FileEntry) => void;
  onTrash: (entry: FileEntry) => void;
  onRestore: (entry: FileEntry) => void;
  onPermanentDelete: (entry: FileEntry) => void;
  onShareFile: (path: string) => void;
}) {
  const limit = pagination?.limit ?? FILE_PAGE_SIZE;
  const offset = pagination?.offset ?? 0;
  const total = pagination?.total ?? entries.length;
  const pageStart = total === 0 ? 0 : offset + 1;
  const pageEnd = Math.min(offset + entries.length, total);
  const pageNumber = Math.floor(offset / limit) + 1;
  const totalPages = Math.max(1, Math.ceil(total / limit));

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="md:hidden">
        <MobileFileList
          entries={entries}
          sharingPath={sharingPath}
          trashView={trashView}
          entryKey={entryKey}
          onOpenFolder={onOpenFolder}
          onSelectFile={onSelectFile}
          onMove={onMove}
          onRename={onRename}
          onTrash={onTrash}
          onRestore={onRestore}
          onPermanentDelete={onPermanentDelete}
          onShareFile={onShareFile}
        />
      </div>
      <div className="hidden min-h-0 flex-1 overflow-auto md:block">
        <table className="w-full min-w-[820px] border-collapse text-sm">
          <thead>
            <tr className="border-b border-border bg-muted/70 text-left text-xs uppercase tracking-normal text-muted-foreground">
              <th className="w-12 px-4 py-3">
                <span className="sr-only">Select</span>
              </th>
              <th className="px-4 py-3 font-semibold">Name</th>
              <th className="px-4 py-3 font-semibold">Type</th>
              <th className="px-4 py-3 text-right font-semibold">Size</th>
              <th className="px-4 py-3 font-semibold">
                {trashView ? 'Trashed' : 'Modified'}
              </th>
              <th className="px-4 py-3 text-right font-semibold">Actions</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => {
              const isDirectory = entry.kind === 'directory';
              const selectionKey = entryKey(entry);
              const isSelected = selectedPaths.has(selectionKey);

              return (
                <tr
                  key={selectionKey}
                  className={cn(
                    'border-b border-border last:border-b-0 hover:bg-muted/30',
                    isSelected && 'bg-muted/50'
                  )}
                  onClick={(event) =>
                    onSelect(entry, {
                      toggle: event.metaKey || event.ctrlKey,
                      range: event.shiftKey,
                    })
                  }
                  onDoubleClick={() => {
                    if (!trashView) {
                      if (isDirectory) onOpenFolder(entry.path);
                      else onSelectFile(entry);
                    }
                  }}
                >
                  <td
                    className="px-4 py-3"
                    onClick={(event) => event.stopPropagation()}
                  >
                    <Checkbox
                      checked={isSelected}
                      onChange={(event) =>
                        onSelect(entry, {
                          toggle: true,
                          range:
                            event.nativeEvent instanceof MouseEvent &&
                            event.nativeEvent.shiftKey,
                        })
                      }
                      aria-label={`Select ${entry.name}`}
                    />
                  </td>
                  <td className="px-4 py-3">
                    <div
                      className="flex max-w-[360px] items-center gap-2 overflow-hidden text-left font-medium"
                      title={entry.path}
                    >
                      {isDirectory ? (
                        <Folder
                          aria-hidden="true"
                          className="h-4 w-4 shrink-0 text-[#11615a]"
                        />
                      ) : entry.thumbnail ? (
                        <img
                          src={getThumbnailUrl(entry.thumbnail.id)}
                          alt=""
                          className="h-7 w-7 shrink-0 rounded object-cover"
                        />
                      ) : (
                        <File
                          aria-hidden="true"
                          className="h-4 w-4 shrink-0 text-[#276c93]"
                        />
                      )}
                      <span className="truncate">{entry.name}</span>
                    </div>
                  </td>
                  <td className="px-4 py-3">
                    <Badge variant={isDirectory ? 'secondary' : 'outline'}>
                      {entry.kind}
                    </Badge>
                  </td>
                  <td className="px-4 py-3 text-right tabular-nums">
                    {formatBytes(
                      isDirectory ? entry.folderSummary?.bytes ?? 0 : entry.size
                    )}
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {formatDate(
                      trashView
                        ? entry.trashedAt ?? entry.updatedAt
                        : entry.updatedAt
                    )}
                  </td>
                  <td
                    className="px-4 py-3 text-right"
                    onClick={(event) => event.stopPropagation()}
                  >
                    {trashView ? (
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => onRestore(entry)}
                        >
                          <RotateCcw className="h-4 w-4" />
                          Restore
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8 text-destructive"
                          aria-label={`Delete ${entry.name} permanently`}
                          onClick={() => onPermanentDelete(entry)}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      </div>
                    ) : (
                      <FileActionMenu
                        entry={entry}
                        sharing={sharingPath === entry.path}
                        onOpen={() =>
                          isDirectory
                            ? onOpenFolder(entry.path)
                            : onSelectFile(entry)
                        }
                        onShare={() => onShareFile(entry.path)}
                        onRename={() => onRename(entry)}
                        onMove={() => onMove(entry)}
                        onTrash={() => onTrash(entry)}
                      />
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="flex flex-col gap-2 border-t border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground md:flex-row md:items-center md:justify-between">
        <span>
          Showing {pageStart}-{pageEnd} of {total}
          {pagination?.query ? ` matching "${pagination.query}"` : ''} ·{' '}
          {pagination?.query ? 'Matching direct files' : 'Direct files'}{' '}
          {formatBytes(visibleBytes)}
        </span>
        {pagination && (
          <div className="flex items-center gap-2">
            <span className="tabular-nums">
              Page {pageNumber} / {totalPages}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={() => onPageChange(Math.max(0, offset - limit))}
              disabled={!pagination?.hasPrev}
            >
              Previous
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => onPageChange(offset + limit)}
              disabled={!pagination?.hasNext}
            >
              Next
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function MobileFileList({
  entries,
  sharingPath,
  trashView,
  entryKey,
  onOpenFolder,
  onSelectFile,
  onMove,
  onRename,
  onTrash,
  onRestore,
  onPermanentDelete,
  onShareFile,
}: {
  entries: FileEntry[];
  sharingPath?: string;
  trashView: boolean;
  entryKey: (entry: FileEntry) => string;
  onOpenFolder: (path: string) => void;
  onSelectFile: (entry: FileEntry) => void;
  onMove: (entry: FileEntry) => void;
  onRename: (entry: FileEntry) => void;
  onTrash: (entry: FileEntry) => void;
  onRestore: (entry: FileEntry) => void;
  onPermanentDelete: (entry: FileEntry) => void;
  onShareFile: (path: string) => void;
}) {
  return (
    <div className="divide-y divide-border">
      {entries.map((entry) => {
        const isDirectory = entry.kind === 'directory';

        return (
          <div key={entryKey(entry)} className="grid gap-3 p-4">
            <button
              type="button"
              className="flex min-w-0 items-start gap-3 text-left"
              onClick={() =>
                !trashView &&
                (isDirectory ? onOpenFolder(entry.path) : onSelectFile(entry))
              }
              title={entry.path}
            >
              {isDirectory ? (
                <Folder
                  aria-hidden="true"
                  className="mt-0.5 h-5 w-5 shrink-0 text-[#11615a]"
                />
              ) : entry.thumbnail ? (
                <img
                  src={getThumbnailUrl(entry.thumbnail.id)}
                  alt=""
                  className="h-10 w-10 shrink-0 rounded object-cover"
                />
              ) : (
                <File
                  aria-hidden="true"
                  className="mt-0.5 h-5 w-5 shrink-0 text-[#276c93]"
                />
              )}
              <span className="min-w-0 flex-1 break-words font-medium leading-6">
                {entry.name}
              </span>
            </button>

            <div className="ml-8 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <Badge variant={isDirectory ? 'secondary' : 'outline'}>
                {entry.kind}
              </Badge>
              <span className="tabular-nums">
                {formatBytes(
                  isDirectory ? entry.folderSummary?.bytes ?? 0 : entry.size
                )}
              </span>
              <span>
                {formatDate(
                  trashView
                    ? entry.trashedAt ?? entry.updatedAt
                    : entry.updatedAt
                )}
              </span>
            </div>

            <div className="ml-8 flex flex-wrap gap-2">
              {trashView ? (
                <>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onRestore(entry)}
                  >
                    <RotateCcw className="h-4 w-4" />
                    Restore
                  </Button>
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => onPermanentDelete(entry)}
                  >
                    Delete permanently
                  </Button>
                </>
              ) : (
                <FileActionMenu
                  entry={entry}
                  sharing={sharingPath === entry.path}
                  onOpen={() =>
                    isDirectory ? onOpenFolder(entry.path) : onSelectFile(entry)
                  }
                  onShare={() => onShareFile(entry.path)}
                  onRename={() => onRename(entry)}
                  onMove={() => onMove(entry)}
                  onTrash={() => onTrash(entry)}
                />
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function FileInspector({
  file,
  sharingPath,
  onClear,
  onShareFile,
  onMove,
  onRename,
  onTrash,
}: {
  file?: FileEntry;
  sharingPath?: string;
  onClear: () => void;
  onShareFile: (path: string) => void;
  onMove: (file: FileEntry) => void;
  onRename: (file: FileEntry) => void;
  onTrash: (file: FileEntry) => void;
}) {
  if (!file) {
    return (
      <aside className="hidden min-h-[360px] overflow-hidden rounded-lg border border-border bg-white xl:block xl:min-h-0">
        <div className="grid h-full min-h-[360px] place-items-center p-6 text-center">
          <div className="grid max-w-xs gap-3">
            <File
              aria-hidden="true"
              className="mx-auto h-10 w-10 text-muted-foreground"
            />
            <div className="grid gap-1">
              <h2 className="text-base font-semibold">No active file</h2>
            </div>
          </div>
        </div>
      </aside>
    );
  }

  return (
    <>
      <aside className="hidden min-h-[480px] overflow-hidden rounded-lg border border-border bg-white xl:block xl:min-h-0">
        <div className="flex h-full min-h-0 flex-col">
          <div className="grid gap-4 border-b border-border p-4">
            <div className="min-w-0">
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <Badge variant="outline">active file</Badge>
                <Badge variant="secondary">{formatBytes(file.size)}</Badge>
              </div>
              <h2
                className="truncate text-base font-semibold"
                title={file.path}
              >
                {file.name}
              </h2>
              <p className="mt-1 break-all text-xs text-muted-foreground">
                {file.path}
              </p>
            </div>
            <dl className="grid grid-cols-[88px_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
              <dt className="text-muted-foreground">Type</dt>
              <dd>file</dd>
              <dt className="text-muted-foreground">Size</dt>
              <dd>{formatBytes(file.size)}</dd>
              <dt className="text-muted-foreground">Modified</dt>
              <dd>{formatDate(file.updatedAt)}</dd>
            </dl>
            <div className="flex flex-wrap gap-2">
              <a
                href={getPreviewUrl(file.path)}
                target="_blank"
                rel="noreferrer"
              >
                <Button variant="outline" size="sm">
                  <Play aria-hidden="true" className="h-4 w-4" />
                  Open
                </Button>
              </a>
              <Button
                variant="outline"
                size="icon"
                aria-label={`Share ${file.name}`}
                onClick={() => onShareFile(file.path)}
                disabled={sharingPath === file.path}
                title={
                  sharingPath === file.path
                    ? `Sharing ${file.name}`
                    : `Share ${file.name}`
                }
                className="h-8 w-8"
              >
                <Share2 aria-hidden="true" className="h-4 w-4" />
              </Button>
              <a href={getDownloadUrl(file.path)}>
                <Button
                  variant="outline"
                  size="icon"
                  aria-label={`Download ${file.name}`}
                  title={`Download ${file.name}`}
                  className="h-8 w-8"
                >
                  <Download aria-hidden="true" className="h-4 w-4" />
                </Button>
              </a>
              <Button variant="outline" size="sm" onClick={() => onMove(file)}>
                <FolderInput className="h-4 w-4" />
                Move
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onRename(file)}
              >
                <Pencil className="h-4 w-4" />
                Rename
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => onTrash(file)}
              >
                <Trash2 className="h-4 w-4" />
                Trash
              </Button>
              <Button variant="ghost" size="sm" onClick={onClear}>
                Clear
              </Button>
            </div>
          </div>
          <div className="flex min-h-0 flex-1 flex-col">
            <div className="border-b border-border px-4 py-3">
              <h3 className="text-sm font-semibold">Preview</h3>
            </div>
            <FilePreview file={file} />
          </div>
        </div>
      </aside>

      <button
        type="button"
        aria-label="Close file preview"
        className="fixed inset-0 z-40 bg-black/25 xl:hidden"
        onClick={onClear}
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={`Preview ${file.name}`}
        className="fixed inset-x-0 bottom-0 z-50 max-h-[86dvh] overflow-hidden rounded-t-2xl border border-border bg-white shadow-2xl xl:hidden"
      >
        <div className="flex max-h-[86dvh] flex-col">
          <div className="grid gap-4 border-b border-border p-4">
            <div className="flex items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="mb-2 flex flex-wrap items-center gap-2">
                  <Badge variant="outline">active file</Badge>
                  <Badge variant="secondary">{formatBytes(file.size)}</Badge>
                </div>
                <h2
                  className="break-words text-base font-semibold"
                  title={file.path}
                >
                  {file.name}
                </h2>
                <p className="mt-1 break-all text-xs text-muted-foreground">
                  {file.path}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon"
                aria-label="Close file preview"
                onClick={onClear}
                className="h-9 w-9 shrink-0"
              >
                <X aria-hidden="true" className="h-4 w-4" />
              </Button>
            </div>
            <dl className="grid grid-cols-[76px_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
              <dt className="text-muted-foreground">Type</dt>
              <dd>file</dd>
              <dt className="text-muted-foreground">Size</dt>
              <dd>{formatBytes(file.size)}</dd>
              <dt className="text-muted-foreground">Modified</dt>
              <dd>{formatDate(file.updatedAt)}</dd>
            </dl>
            <div className="flex flex-wrap gap-2">
              <a
                href={getPreviewUrl(file.path)}
                target="_blank"
                rel="noreferrer"
              >
                <Button variant="outline" size="sm">
                  <Play aria-hidden="true" className="h-4 w-4" />
                  Open
                </Button>
              </a>
              <Button
                variant="outline"
                size="icon"
                aria-label={`Share ${file.name}`}
                onClick={() => onShareFile(file.path)}
                disabled={sharingPath === file.path}
                title={
                  sharingPath === file.path
                    ? `Sharing ${file.name}`
                    : `Share ${file.name}`
                }
                className="h-9 w-9"
              >
                <Share2 aria-hidden="true" className="h-4 w-4" />
              </Button>
              <a href={getDownloadUrl(file.path)}>
                <Button
                  variant="outline"
                  size="icon"
                  aria-label={`Download ${file.name}`}
                  title={`Download ${file.name}`}
                  className="h-9 w-9"
                >
                  <Download aria-hidden="true" className="h-4 w-4" />
                </Button>
              </a>
              <Button variant="outline" size="sm" onClick={() => onMove(file)}>
                <FolderInput className="h-4 w-4" />
                Move
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onRename(file)}
              >
                <Pencil className="h-4 w-4" />
                Rename
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => onTrash(file)}
              >
                <Trash2 className="h-4 w-4" />
                Trash
              </Button>
            </div>
          </div>
          <div className="flex min-h-0 flex-1 flex-col">
            <div className="border-b border-border px-4 py-3">
              <h3 className="text-sm font-semibold">Preview</h3>
            </div>
            <div className="flex min-h-[260px] flex-1 flex-col overflow-hidden">
              <FilePreview file={file} />
            </div>
          </div>
        </div>
      </aside>
    </>
  );
}

function FilePreview({ file }: { file: FileEntry }) {
  const previewUrl = getPreviewUrl(file.path);
  const previewKind = getPreviewKind(file);
  const [textPreview, setTextPreview] = useState<TextPreviewState>({
    status: 'idle',
  });

  useEffect(() => {
    if (previewKind !== 'text') {
      setTextPreview({ status: 'idle' });
      return;
    }

    const controller = new AbortController();
    setTextPreview({ status: 'loading' });

    fetch(previewUrl, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) {
          throw new Error(`${response.status} ${response.statusText}`);
        }
        return response.text();
      })
      .then((content) => {
        const formatted = formatTextPreview(file, content);
        setTextPreview({
          status: 'ready',
          content: formatted.slice(0, TEXT_PREVIEW_LIMIT),
          truncated: formatted.length > TEXT_PREVIEW_LIMIT,
        });
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        setTextPreview({
          status: 'error',
          message:
            error instanceof Error ? error.message : 'Unable to load preview',
        });
      });

    return () => controller.abort();
  }, [file, previewKind, previewUrl]);

  if (file.thumbnail) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center overflow-auto bg-muted/20 p-4">
        <img
          src={getThumbnailUrl(file.thumbnail.id)}
          alt={`${file.name} 的壓縮檔縮圖`}
          className="max-h-full max-w-full rounded-md object-contain shadow-sm"
        />
      </div>
    );
  }

  if (previewKind === 'image') {
    return (
      <div className="grid min-h-0 flex-1 place-items-center overflow-auto bg-muted/20 p-4">
        <img
          src={previewUrl}
          alt={file.name}
          className="max-h-full max-w-full rounded-md object-contain"
        />
      </div>
    );
  }

  if (previewKind === 'video') {
    return (
      <div className="grid min-h-0 flex-1 place-items-center overflow-auto bg-muted/20 p-4">
        <video
          src={previewUrl}
          controls
          preload="metadata"
          className="max-h-full max-w-full rounded-md bg-black"
        />
      </div>
    );
  }

  if (previewKind === 'text') {
    if (textPreview.status === 'loading') {
      return (
        <div className="grid gap-3 p-4">
          <Skeleton className="h-5 w-2/3" />
          <Skeleton className="h-5" />
          <Skeleton className="h-5 w-5/6" />
        </div>
      );
    }

    if (textPreview.status === 'error') {
      return (
        <div className="grid min-h-0 flex-1 place-items-center p-6 text-center text-sm text-muted-foreground">
          Preview unavailable: {textPreview.message}
        </div>
      );
    }

    if (textPreview.status === 'ready') {
      return (
        <div className="min-h-0 flex-1 overflow-auto bg-[#111827] p-4 text-xs leading-5 text-[#e5e7eb]">
          <pre className="whitespace-pre-wrap break-words font-mono">
            {textPreview.content}
          </pre>
          {textPreview.truncated && (
            <p className="mt-4 border-t border-white/15 pt-3 text-[#cbd5e1]">
              Preview truncated at {formatBytes(TEXT_PREVIEW_LIMIT)}.
            </p>
          )}
        </div>
      );
    }
  }

  return (
    <div className="grid min-h-0 flex-1 place-items-center p-6 text-center">
      <div className="grid max-w-xs gap-3">
        <File
          aria-hidden="true"
          className="mx-auto h-10 w-10 text-muted-foreground"
        />
        <div className="grid gap-1">
          <h3 className="text-sm font-semibold">No preview available</h3>
        </div>
      </div>
    </div>
  );
}

function getPreviewKind(file: FileEntry) {
  const extension = fileExtension(file.name);
  if (
    ['avif', 'bmp', 'gif', 'jpeg', 'jpg', 'png', 'svg', 'webp'].includes(
      extension
    )
  ) {
    return 'image';
  }

  if (['m4v', 'mov', 'mp4', 'ogg', 'ogv', 'webm'].includes(extension)) {
    return 'video';
  }

  if (
    [
      'c',
      'conf',
      'cpp',
      'cs',
      'css',
      'csv',
      'go',
      'h',
      'html',
      'java',
      'js',
      'json',
      'jsx',
      'log',
      'md',
      'py',
      'rs',
      'sh',
      'sql',
      'toml',
      'ts',
      'tsx',
      'txt',
      'xml',
      'yaml',
      'yml',
    ].includes(extension)
  ) {
    return 'text';
  }

  return 'unsupported';
}

function formatTextPreview(file: FileEntry, content: string) {
  if (fileExtension(file.name) !== 'json') {
    return content;
  }

  try {
    return JSON.stringify(JSON.parse(content), null, 2);
  } catch {
    return content;
  }
}

function fileExtension(name: string) {
  const index = name.lastIndexOf('.');
  if (index < 0 || index === name.length - 1) {
    return '';
  }
  return name.slice(index + 1).toLowerCase();
}

function EmptyState({ query }: { query: string }) {
  return (
    <div className="grid min-h-[320px] place-items-center p-8 text-center">
      <div className="grid max-w-sm gap-3">
        <Folder
          aria-hidden="true"
          className="mx-auto h-10 w-10 text-muted-foreground"
        />
        <div className="grid gap-1">
          <h2 className="text-base font-semibold">
            {query.trim() ? 'No matching entries' : 'This folder is empty'}
          </h2>
          <p className="text-sm leading-6 text-muted-foreground">
            No entries are available for the current view.
          </p>
        </div>
      </div>
    </div>
  );
}

function LoadingTable() {
  return (
    <div className="grid gap-3 p-4">
      {Array.from({ length: 6 }).map((_, index) => (
        <div
          key={index}
          className="grid grid-cols-[minmax(0,1fr)_120px_120px] gap-4"
        >
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ))}
    </div>
  );
}
