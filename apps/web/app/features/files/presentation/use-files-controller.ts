import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from 'react';

import type { FileBrowserView } from '../application/files-controller';
import type { FileEntry } from '../domain/files';
import { fileBrowserPath } from './file-route';
import {
  type FileViewMode,
  readFileViewMode,
  writeFileViewMode,
} from './file-view-mode';
import type {
  FilesControllerDependencies,
  FilesUploadQueue,
} from './files-presentation-contracts';
import { useFileSelection } from './use-file-selection';

type UseFilesControllerOptions = {
  currentPath: string;
  dependencies: FilesControllerDependencies;
  navigate: (path: string) => void;
  uploadQueue: FilesUploadQueue;
  view: FileBrowserView;
};

const SEARCH_DEBOUNCE_MS = 250;
const EMPTY_FILE_ENTRIES: FileEntry[] = [];

export { FILE_PAGE_SIZE } from '../application/files-controller';

export function useFilesController({
  currentPath,
  dependencies,
  navigate,
  uploadQueue,
  view,
}: UseFilesControllerOptions) {
  const { controller, createShareDraft, getPreviewUrl, resolveAppPath } =
    dependencies;
  const snapshot = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
    controller.getSnapshot
  );
  const [query, setQuery] = useState('');
  const [fileViewMode, setFileViewMode] = useState<FileViewMode>(() =>
    readFileViewMode(
      typeof window === 'undefined' ? undefined : window.localStorage
    )
  );
  const [shareError, setShareError] = useState<string>();
  const [sharingPath, setSharingPath] = useState<string>();
  const [selectedFile, setSelectedFile] = useState<FileEntry>();
  const [showUpload, setShowUpload] = useState(false);
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
  const failedUploadKeysRef = useRef(new Set<string>());

  useEffect(() => {
    controller.enter(currentPath, view);
  }, [controller, currentPath, view]);

  useEffect(() => () => controller.leave(), [controller]);

  useEffect(() => {
    const timeout = window.setTimeout(
      () => controller.setSearchQuery(query),
      SEARCH_DEBOUNCE_MS
    );
    return () => window.clearTimeout(timeout);
  }, [controller, query]);

  useEffect(() => {
    controller.observeUploads([...uploadQueue.items]);
  }, [controller, uploadQueue.items]);

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

  const visibleEntries = snapshot.files?.entries ?? EMPTY_FILE_ENTRIES;
  const currentEntries =
    view === 'trash' ? snapshot.trashEntries : visibleEntries;
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

  const changeFileViewMode = useCallback((mode: FileViewMode) => {
    setFileViewMode(mode);
    writeFileViewMode(
      typeof window === 'undefined' ? undefined : window.localStorage,
      mode
    );
  }, []);

  const selectFile = useCallback((entry: FileEntry) => {
    setSelectedFile(entry);
    setShareError(undefined);
  }, []);

  const openFolder = useCallback(
    (path: string) => {
      setSelectedFile(undefined);
      setQuery('');
      navigate(fileBrowserPath(path));
    },
    [navigate]
  );

  useEffect(() => {
    clearSelection();
    setSelectedFile(undefined);
  }, [clearSelection, currentPath, view]);

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
      ) {
        return;
      }
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
  const beginRename = (entry: FileEntry) => setRenameEntry(entry);

  const runRename = async (name: string) => {
    const entry = renameEntry;
    if (!entry) return;
    await controller.rename(entry.path, name);
    setRenameEntry(undefined);
    selection.clear();
    setSelectedFile(undefined);
  };

  const runMove = async (destination: string) => {
    if (await controller.move(actionPaths, destination)) {
      setShowMove(false);
      selection.clear();
    }
  };

  const runTrash = async () => {
    if (await controller.moveToTrash(actionPaths)) {
      setShowTrashConfirm(false);
      selection.clear();
      setSelectedFile(undefined);
    }
  };

  const runRestore = async () => {
    if (await controller.restore(selectedTrashIds)) selection.clear();
  };

  const restoreEntry = async (trashId: string) => {
    if (await controller.restore([trashId])) selection.clear();
  };

  const runPermanentDelete = async () => {
    const trashIds =
      actionTrashIds.length > 0 ? actionTrashIds : selectedTrashIds;
    if (await controller.deletePermanently(trashIds)) {
      setShowPermanentConfirm(false);
      selection.clear();
    }
  };

  const runEmptyTrash = async () => {
    if (await controller.emptyTrash()) {
      setShowEmptyConfirm(false);
      selection.clear();
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
          popup.location.replace(resolveAppPath(sharePath));
        } else {
          window.location.href = resolveAppPath(sharePath);
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
    [createShareDraft, resolveAppPath]
  );

  const hasActivityDock = Boolean(
    snapshot.error ||
      shareError ||
      snapshot.actionError ||
      snapshot.activeOperation ||
      uploadQueue.items.length > 0 ||
      selection.selected.size > 0
  );

  return {
    listing: {
      changeFileViewMode,
      currentEntries,
      currentPagination: snapshot.files?.pagination,
      fileViewMode,
      folderSummary: snapshot.files?.folderSummary,
      openFolder,
      query,
      refresh: () => controller.refresh(),
      setPageOffset: (offset: number) => controller.setPageOffset(offset),
      setQuery,
      state: {
        status: snapshot.status,
        files: snapshot.files,
        loading: snapshot.loading,
        error: snapshot.error,
      },
      totalVisibleBytes: snapshot.files?.visibleBytes ?? 0,
    },
    trash: {
      actionTrashIds,
      loadTrash: () => controller.loadTrash(),
      restoreEntry,
      selectedTrashIds,
      setActionTrashIds,
      setShowEmptyConfirm,
      setShowPermanentConfirm,
      showEmptyConfirm,
      showPermanentConfirm,
      trashEntries: snapshot.trashEntries,
      trashLoading: snapshot.trashLoading,
    },
    selection: {
      entryKey,
      selectFile,
      selectedFile,
      selection,
      setSelectedFile,
    },
    operations: {
      actionError: snapshot.actionError,
      actionPaths,
      activeOperation: snapshot.activeOperation,
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
  };
}
