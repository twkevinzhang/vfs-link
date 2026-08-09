import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { NavigateFunction } from 'react-router';

import type { FilesControllerDependencies } from '../features/files/application/files-controller-dependencies';
import type { PreparedArchiveBatch } from '../features/upload/application/upload-contracts';
import { watchFileOperation } from '../features/files/application/watch-file-operation';
import { appPath } from '../lib/base-path';
import { fileBrowserPath } from '../lib/file-route';
import {
  type FileViewMode,
  readFileViewMode,
  writeFileViewMode,
} from '../lib/file-view-mode';
import { normalizePath } from '../lib/format';
import type {
  FileEntry,
  FileOperationResponse,
  FilesResponse,
  StatusResponse,
  TrashEntry,
} from '../features/files/domain/files';
import { useFileSelection } from './use-file-selection';
import {
  useBackgroundUploadQueue,
  type UploadQueueItem,
} from './use-upload-queue';

type LoadState = {
  status?: StatusResponse;
  files?: FilesResponse;
  loading: boolean;
  error?: string;
};

type FileBrowserView = 'files' | 'trash';

type UseFilesControllerOptions = {
  currentPath: string;
  dependencies: FilesControllerDependencies;
  navigate: NavigateFunction;
  view: FileBrowserView;
};

export const FILE_PAGE_SIZE = 100;

const SEARCH_DEBOUNCE_MS = 250;
const UPLOAD_REFRESH_DEBOUNCE_MS = 300;
const EMPTY_FILE_ENTRIES: FileEntry[] = [];

type PendingArchiveBatch = PreparedArchiveBatch & {
  paths: Set<string>;
  completed: Set<string>;
  processing: boolean;
};

export function useFilesController({
  currentPath,
  dependencies,
  navigate,
  view,
}: UseFilesControllerOptions) {
  const {
    createShareDraft,
    createThumbnail,
    deleteThumbnails,
    deleteTrash,
    emptyTrash,
    getFileOperation,
    getFiles,
    getPreviewUrl,
    getStatus,
    getTrash,
    moveFiles,
    moveFilesToTrash,
    removeArchiveTemporaryFiles,
    renameFile,
    restoreTrash,
  } = dependencies;
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
  const fileOperationAbortRef = useRef<AbortController | undefined>(undefined);
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
  }, [getStatus]);

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
    [getFiles]
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

  useEffect(
    () => () => {
      fileOperationAbortRef.current?.abort();
      fileOperationAbortRef.current = undefined;
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
  }, [
    createThumbnail,
    deleteThumbnails,
    handleUploadComplete,
    loadFiles,
    removeArchiveTemporaryFiles,
    uploadQueue.items,
  ]);

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
  }, [getTrash]);

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
  }, [getPreviewUrl, openFolder, selectedEntries, selection, view]);

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
    fileOperationAbortRef.current?.abort();
    const controller = new AbortController();
    fileOperationAbortRef.current = controller;
    try {
      const operation = await watchFileOperation({
        id,
        signal: controller.signal,
        fetchOperation: (operationId, signal) =>
          getFileOperation(operationId, { signal }),
        onUpdate: (nextOperation) => {
          if (fileOperationAbortRef.current === controller) {
            setActiveOperation(nextOperation);
          }
        },
      });
      if (fileOperationAbortRef.current !== controller) {
        return;
      }
      setActiveOperation(undefined);
      if (operation.status === 'completed') {
        selection.clear();
        setSelectedFile(undefined);
        refresh();
        void loadTrash();
        void loadStatus();
      } else {
        setActionError(operation.error || 'Background operation failed');
      }
    } catch (error) {
      if (
        controller.signal.aborted ||
        fileOperationAbortRef.current !== controller
      ) {
        return;
      }
      setActiveOperation(undefined);
      setActionError(
        error instanceof Error
          ? error.message
          : 'Unable to monitor background operation'
      );
    } finally {
      if (fileOperationAbortRef.current === controller) {
        fileOperationAbortRef.current = undefined;
      }
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
  const restoreEntry = async (trashId: string) => {
    await restoreTrash([trashId])
      .then(() => {
        selection.clear();
        void loadTrash();
        void loadStatus();
      })
      .catch((error) =>
        setActionError(
          error instanceof Error ? error.message : 'Unable to restore item'
        )
      );
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

  const shareFile = useCallback(
    async (path: string) => {
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
    },
    [createShareDraft]
  );

  const hasActivityDock = Boolean(
    state.error ||
      shareError ||
      actionError ||
      activeOperation ||
      uploadQueue.items.length > 0 ||
      !uploadQueue.isUploadLeader ||
      selection.selected.size > 0
  );

  return {
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
    sharing: {
      shareError,
      shareFile,
      sharingPath,
    },
    hasActivityDock,
  };
}
