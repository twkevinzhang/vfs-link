import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';

import {
  cancelUpload,
  completeUpload,
  createUpload,
  getUploadSession,
  preflightUploads,
  putUploadChunk,
} from '../lib/api';
import { normalizePath } from '../lib/format';
import type { UploadCandidate } from '../lib/folder-upload';
import {
  MAX_CONCURRENT_UPLOADS,
  duplicateLogicPaths,
  fileFingerprint,
  isOffsetConflict,
  isRetryAllEligible,
  isTransientUploadError,
  isUploadTargetChanged,
  matchesFingerprint,
  nextChunkRange,
  nextRunnableUploadKeys,
  retryDelayMs,
  shouldAutomaticallyRetry,
  uploadStateNeedsSource,
  type UploadFingerprint,
} from '../lib/upload-queue-core';
import {
  loadUploadQueue,
  saveUploadQueue,
  type PersistedUploadItem,
  type PersistedUploadState,
  type UploadSourceSnapshot,
} from '../lib/upload-queue-storage';
import type {
  UploadPreflightExisting,
  UploadPreflightStatus,
  UploadSession,
} from '../types/upload';

const SOURCE_CHECK_INTERVAL_MS = 15_000;

export type UploadQueueState = PersistedUploadState;

export type UploadQueueItem = {
  key: string;
  /** One user selection event; bulk conflict actions never cross this boundary. */
  batchId: string;
  file?: File;
  fileHandle?: FileSystemFileHandle;
  fingerprint: UploadFingerprint;
  contentType: string;
  relativePath: string;
  /** The folder selected when the item was added, rather than the current view. */
  destinationPath: string;
  /** Fully resolved storage path, also captured when the item was added. */
  logicPath: string;
  uploadedBytes: number;
  progress: number;
  state: UploadQueueState;
  error?: string;
  session?: UploadSession;
  /** Whether a direct child with this name existed when the item was added. */
  overwrite: boolean;
  /** Opaque target version captured by the most recent server preflight. */
  targetVersion?: string;
  targetStatus?: UploadPreflightStatus;
  existingTarget?: UploadPreflightExisting;
  /** Multiple source files in this batch resolve to the same logical path. */
  localDuplicate: boolean;
  archiveGroupId?: string;
  retryCount: number;
  retryEligible: boolean;
  retryAt?: number;
  missingFromState?: Exclude<UploadQueueState, 'local-missing'>;
};

type UploadQueueSummary = {
  total: number;
  queued: number;
  checking: number;
  needsDecision: number;
  skipped: number;
  uploading: number;
  retrying: number;
  paused: number;
  complete: number;
  failed: number;
  localMissing: number;
  totalBytes: number;
  uploadedBytes: number;
  /** Byte-weighted progress across every retained queue item. */
  progress: number;
};

type UseUploadQueueOptions = {
  onItemComplete?: (item: UploadQueueItem) => void;
};

type PermissionAwareFileHandle = FileSystemFileHandle & {
  queryPermission?: (descriptor: { mode: 'read' }) => Promise<PermissionState>;
  requestPermission?: (descriptor: {
    mode: 'read';
  }) => Promise<PermissionState>;
};

type HandleFileResult =
  | { status: 'available'; file: File }
  | { status: 'permission-required' }
  | { status: 'missing' };

function makeQueueKey(candidate: UploadCandidate) {
  const randomId =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${candidate.relativePath}-${candidate.file.size}-${candidate.file.lastModified}-${randomId}`;
}

function makeBatchId() {
  return typeof crypto !== 'undefined' &&
    typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `batch-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Upload failed';
}

function progressFor(uploadedBytes: number, size: number) {
  return size === 0
    ? uploadedBytes === 0
      ? 0
      : 100
    : (uploadedBytes / size) * 100;
}

function toPersisted(item: UploadQueueItem): PersistedUploadItem {
  return {
    key: item.key,
    batchId: item.batchId,
    relativePath: item.relativePath,
    destinationPath: item.destinationPath,
    logicPath: item.logicPath,
    fingerprint: item.fingerprint,
    contentType: item.contentType,
    fileHandle: item.fileHandle,
    uploadedBytes: item.uploadedBytes,
    state: item.state,
    error: item.error,
    session: item.session,
    overwrite: item.overwrite,
    targetVersion: item.targetVersion,
    targetStatus: item.targetStatus,
    existingTarget: item.existingTarget,
    localDuplicate: item.localDuplicate,
    archiveGroupId: item.archiveGroupId,
    retryCount: item.retryCount,
    retryEligible: item.retryEligible,
    missingFromState: item.missingFromState,
  };
}

function waitForRetry(delay: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(resolve, delay);
    signal.addEventListener(
      'abort',
      () => {
        window.clearTimeout(timer);
        reject(new DOMException('Upload paused', 'AbortError'));
      },
      { once: true }
    );
  });
}

async function inspectHandleFile(
  handle: FileSystemFileHandle
): Promise<HandleFileResult> {
  const permissionHandle = handle as PermissionAwareFileHandle;
  if (permissionHandle.queryPermission) {
    try {
      const permission = await permissionHandle.queryPermission({
        mode: 'read',
      });
      if (permission === 'denied') return { status: 'missing' };
      if (permission !== 'granted') return { status: 'permission-required' };
    } catch (error) {
      if ((error as { name?: string }).name === 'NotAllowedError') {
        return { status: 'permission-required' };
      }
      return { status: 'missing' };
    }
  }
  try {
    return { status: 'available', file: await handle.getFile() };
  } catch (error) {
    return (error as { name?: string }).name === 'NotAllowedError'
      ? { status: 'permission-required' }
      : { status: 'missing' };
  }
}

/**
 * Persistent, chunked upload coordinator. Server-confirmed uploadedSize is the
 * only resume cursor, so an aborted or ambiguous request is always reconciled
 * before another chunk is sent.
 */
export function useUploadQueue({ onItemComplete }: UseUploadQueueOptions = {}) {
  const [items, setItems] = useState<UploadQueueItem[]>([]);
  const [hydrated, setHydrated] = useState(false);
  const [globallyPaused, setGloballyPaused] = useState(false);
  const itemsRef = useRef(items);
  const globallyPausedRef = useRef(globallyPaused);
  const hydratedRef = useRef(false);
  const mountedRef = useRef(true);
  const onItemCompleteRef = useRef(onItemComplete);
  const runningKeysRef = useRef(new Set<string>());
  const cancelledKeysRef = useRef(new Set<string>());
  const pauseRequestedKeysRef = useRef(new Set<string>());
  const abortControllersRef = useRef(new Map<string, AbortController>());
  const progressFrameRef = useRef<number | undefined>(undefined);
  const pendingProgressRef = useRef(new Map<string, number>());
  const persistenceRef = useRef(Promise.resolve());
  const scheduleRef = useRef<(() => void) | undefined>(undefined);
  const preflightRef = useRef<((keys: string[]) => Promise<void>) | undefined>(
    undefined
  );

  onItemCompleteRef.current = onItemComplete;
  globallyPausedRef.current = globallyPaused;

  const persist = useCallback(
    (nextItems = itemsRef.current, nextPaused = globallyPausedRef.current) => {
      if (!hydratedRef.current) return;
      const snapshot = nextItems.map(toPersisted);
      const sources: UploadSourceSnapshot[] = nextItems.map((item) => ({
        key: item.key,
        file: item.file,
        fileHandle: item.fileHandle,
        retainFile: !['complete', 'skipped', 'local-missing'].includes(
          item.state
        ),
      }));
      persistenceRef.current = persistenceRef.current
        .catch(() => undefined)
        .then(() => saveUploadQueue(snapshot, nextPaused, sources));
    },
    []
  );

  const updateItems = useCallback(
    (
      update: (current: UploadQueueItem[]) => UploadQueueItem[],
      options: { persist?: boolean } = {}
    ) => {
      if (!mountedRef.current) return;
      const next = update(itemsRef.current);
      itemsRef.current = next;
      setItems(next);
      if (options.persist !== false) persist(next);
    },
    [persist]
  );

  const updateItem = useCallback(
    (
      key: string,
      update: (item: UploadQueueItem) => UploadQueueItem,
      options?: { persist?: boolean }
    ) => {
      updateItems(
        (current) =>
          current.map((item) => (item.key === key ? update(item) : item)),
        options
      );
    },
    [updateItems]
  );

  const cleanupUploadSession = useCallback((sessionId: string) => {
    void cancelUpload(sessionId).catch(() => undefined);
  }, []);

  const flushProgress = useCallback(() => {
    progressFrameRef.current = undefined;
    const progress = pendingProgressRef.current;
    pendingProgressRef.current = new Map();
    if (progress.size === 0) return;
    updateItems(
      (current) =>
        current.map((item) => {
          const uploadedBytes = progress.get(item.key);
          if (uploadedBytes === undefined || item.state === 'complete')
            return item;
          return {
            ...item,
            progress: Math.max(
              item.progress,
              Math.min(100, progressFor(uploadedBytes, item.fingerprint.size))
            ),
          };
        }),
      { persist: false }
    );
  }, [updateItems]);

  const queueProgress = useCallback(
    (key: string, uploadedBytes: number) => {
      pendingProgressRef.current.set(key, uploadedBytes);
      if (
        progressFrameRef.current !== undefined ||
        typeof window === 'undefined'
      )
        return;
      progressFrameRef.current = window.requestAnimationFrame(flushProgress);
    },
    [flushProgress]
  );

  const markLocalMissing = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || item.state === 'local-missing') return;
      pauseRequestedKeysRef.current.add(key);
      abortControllersRef.current.get(key)?.abort();
      updateItem(key, (current) => ({
        ...current,
        file: undefined,
        state: 'local-missing',
        missingFromState:
          current.state === 'local-missing'
            ? current.missingFromState
            : current.state,
        retryEligible: false,
        retryAt: undefined,
        error: '已從本機移除',
        progress: progressFor(current.uploadedBytes, current.fingerprint.size),
      }));
    },
    [updateItem]
  );

  const pause = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || !['queued', 'uploading', 'retrying'].includes(item.state))
        return;
      pauseRequestedKeysRef.current.add(key);
      abortControllersRef.current.get(key)?.abort();
      updateItem(key, (current) => ({
        ...current,
        state: 'paused',
        retryAt: undefined,
        error: undefined,
        progress: progressFor(current.uploadedBytes, current.fingerprint.size),
      }));
    },
    [updateItem]
  );

  const resume = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || item.state !== 'paused' || (!item.file && !item.fileHandle))
        return;
      if (globallyPausedRef.current) {
        globallyPausedRef.current = false;
        setGloballyPaused(false);
        persist(itemsRef.current, false);
      }
      pauseRequestedKeysRef.current.delete(key);
      updateItem(key, (current) => ({
        ...current,
        state: 'queued',
        error: undefined,
      }));
      scheduleRef.current?.();
    },
    [persist, updateItem]
  );

  const pauseAll = useCallback(() => {
    globallyPausedRef.current = true;
    setGloballyPaused(true);
    for (const item of itemsRef.current) {
      if (['queued', 'uploading', 'retrying'].includes(item.state)) {
        pauseRequestedKeysRef.current.add(item.key);
        abortControllersRef.current.get(item.key)?.abort();
      }
    }
    updateItems((current) =>
      current.map((item) =>
        ['queued', 'uploading', 'retrying'].includes(item.state)
          ? {
              ...item,
              state: 'paused',
              retryAt: undefined,
              error: undefined,
              progress: progressFor(item.uploadedBytes, item.fingerprint.size),
            }
          : item
      )
    );
    persist(itemsRef.current, true);
  }, [persist, updateItems]);

  const resumeAll = useCallback(() => {
    globallyPausedRef.current = false;
    setGloballyPaused(false);
    pauseRequestedKeysRef.current.clear();
    updateItems((current) =>
      current.map((item) =>
        item.state === 'paused' && (item.file || item.fileHandle)
          ? { ...item, state: 'queued', error: undefined }
          : item
      )
    );
    persist(itemsRef.current, false);
    scheduleRef.current?.();
  }, [persist, updateItems]);

  const dismiss = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || ['queued', 'uploading', 'retrying'].includes(item.state))
        return;
      if (item.session && item.state !== 'complete') {
        cleanupUploadSession(item.session.id);
      }
      updateItems((current) =>
        current.filter((candidate) => candidate.key !== key)
      );
    },
    [cleanupUploadSession, updateItems]
  );

  const cancel = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || item.state === 'complete') return;
      cancelledKeysRef.current.add(key);
      abortControllersRef.current.get(key)?.abort();
      if (item.session) cleanupUploadSession(item.session.id);
      pendingProgressRef.current.delete(key);
      updateItems((current) =>
        current.filter((candidate) => candidate.key !== key)
      );
      if (!runningKeysRef.current.has(key)) {
        cancelledKeysRef.current.delete(key);
      }
      scheduleRef.current?.();
    },
    [cleanupUploadSession, updateItems]
  );

  const cancelAll = useCallback(() => {
    const keys = itemsRef.current
      .filter((item) =>
        [
          'checking',
          'needs-decision',
          'queued',
          'uploading',
          'retrying',
          'paused',
        ].includes(item.state)
      )
      .map((item) => item.key);
    for (const key of keys) cancel(key);
  }, [cancel]);

  const retry = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (
        !item ||
        item.state !== 'failed' ||
        !item.retryEligible ||
        (!item.file && !item.fileHandle)
      )
        return;
      pauseRequestedKeysRef.current.delete(key);
      const needsPreflight = !item.targetStatus && !item.session;
      updateItem(key, (current) => ({
        ...current,
        state: needsPreflight
          ? 'checking'
          : globallyPausedRef.current
          ? 'paused'
          : 'queued',
        error: undefined,
        retryCount: 0,
        retryAt: undefined,
      }));
      if (needsPreflight) void preflightRef.current?.([key]);
      else scheduleRef.current?.();
    },
    [updateItem]
  );

  const retryAll = useCallback(() => {
    const retryableKeys = itemsRef.current
      .filter((item) =>
        isRetryAllEligible(
          item.state,
          item.retryEligible,
          Boolean(item.file || item.fileHandle)
        )
      )
      .map((item) => item.key);
    if (retryableKeys.length === 0) return;
    const retryable = new Set(retryableKeys);
    updateItems((current) =>
      current.map((item) =>
        retryable.has(item.key)
          ? {
              ...item,
              state: globallyPausedRef.current ? 'paused' : 'queued',
              error: undefined,
              retryCount: 0,
              retryAt: undefined,
            }
          : item
      )
    );
    scheduleRef.current?.();
  }, [updateItems]);

  const reconnect = useCallback(
    (
      key: string,
      file: File,
      fileHandle?: FileSystemFileHandle
    ): string | undefined => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item) return '找不到上傳項目。';
      if (!matchesFingerprint(file, item.fingerprint)) {
        return '選取的檔案與原始來源不相符。';
      }
      const resumeState =
        item.missingFromState === 'complete'
          ? 'complete'
          : item.missingFromState === 'needs-decision'
          ? 'needs-decision'
          : item.missingFromState === 'checking'
          ? 'checking'
          : globallyPausedRef.current
          ? 'paused'
          : 'queued';
      pauseRequestedKeysRef.current.delete(key);
      updateItem(key, (current) => ({
        ...current,
        file,
        fileHandle,
        state: resumeState,
        missingFromState: undefined,
        retryEligible: true,
        error: undefined,
      }));
      if (resumeState === 'checking') void preflightRef.current?.([key]);
      else scheduleRef.current?.();
      return undefined;
    },
    [updateItem]
  );

  const authorizeSource = useCallback(
    async (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item?.fileHandle) return;
      const handle = item.fileHandle as PermissionAwareFileHandle;
      try {
        const permission = handle.requestPermission
          ? await handle.requestPermission({ mode: 'read' })
          : 'granted';
        if (permission !== 'granted') {
          updateItem(key, (current) => ({
            ...current,
            state: 'paused',
            error: '需要來源檔案的讀取權限才能繼續上傳。',
          }));
          return;
        }
        const file = await item.fileHandle.getFile();
        if (!matchesFingerprint(file, item.fingerprint)) {
          markLocalMissing(key);
          return;
        }
        pauseRequestedKeysRef.current.delete(key);
        const resumeState =
          item.missingFromState === 'needs-decision'
            ? 'needs-decision'
            : item.missingFromState === 'checking'
            ? 'checking'
            : globallyPausedRef.current
            ? 'paused'
            : 'queued';
        updateItem(key, (current) => ({
          ...current,
          file,
          state: resumeState,
          missingFromState: undefined,
          retryEligible: true,
          error: undefined,
        }));
        if (resumeState === 'checking') void preflightRef.current?.([key]);
        else scheduleRef.current?.();
      } catch (error) {
        if ((error as { name?: string }).name === 'NotFoundError') {
          markLocalMissing(key);
          return;
        }
        updateItem(key, (current) => ({
          ...current,
          state: 'paused',
          error: '無法取得來源檔案權限，請重新允許或選擇原始檔案。',
        }));
      }
    },
    [markLocalMissing, updateItem]
  );

  const runPreflight = useCallback(
    async (keys: string[]) => {
      const requestedKeys = new Set(keys);
      const candidates = itemsRef.current.filter(
        (item) => requestedKeys.has(item.key) && item.state === 'checking'
      );
      if (candidates.length === 0) return;
      try {
        const response = await preflightUploads(
          candidates.map((item) => ({
            clientId: item.key,
            path: item.logicPath,
          }))
        );
        const results = new Map(
          response.items.map((result) => [result.clientId, result] as const)
        );
        updateItems((current) =>
          current.map((item) => {
            if (!requestedKeys.has(item.key) || item.state !== 'checking') {
              return item;
            }
            const result = results.get(item.key);
            if (!result) {
              return {
                ...item,
                state: 'failed',
                retryEligible: true,
                error: 'Preflight response did not include this upload.',
              };
            }
            const needsDecision =
              item.localDuplicate || result.status !== 'available';
            return {
              ...item,
              targetStatus: result.status,
              targetVersion: result.targetVersion,
              existingTarget: result.existing,
              overwrite: false,
              state: needsDecision
                ? 'needs-decision'
                : globallyPausedRef.current
                ? 'paused'
                : 'queued',
              retryCount: 0,
              retryEligible: false,
              error:
                result.status === 'directory'
                  ? '目的路徑是資料夾，無法用檔案取代。'
                  : undefined,
            };
          })
        );
        scheduleRef.current?.();
      } catch (error) {
        updateItems((current) =>
          current.map((item) =>
            requestedKeys.has(item.key) && item.state === 'checking'
              ? {
                  ...item,
                  state: 'failed',
                  retryEligible: isTransientUploadError(error),
                  error: errorMessage(error),
                }
              : item
          )
        );
      }
    },
    [updateItems]
  );

  preflightRef.current = runPreflight;

  const replaceOne = useCallback(
    (key: string) => {
      const selected = itemsRef.current.find((item) => item.key === key);
      if (
        !selected ||
        selected.state !== 'needs-decision' ||
        selected.targetStatus === 'directory'
      ) {
        return;
      }
      updateItems((current) =>
        current.map((item) => {
          const isDuplicatePeer =
            selected.localDuplicate &&
            item.key !== selected.key &&
            item.batchId === selected.batchId &&
            item.logicPath === selected.logicPath &&
            item.state === 'needs-decision';
          if (isDuplicatePeer) {
            return {
              ...item,
              state: 'skipped',
              retryEligible: false,
              error: undefined,
            };
          }
          if (item.key !== selected.key) return item;
          return {
            ...item,
            overwrite: item.targetStatus === 'conflict',
            state: globallyPausedRef.current ? 'paused' : 'queued',
            retryEligible: false,
            error: undefined,
          };
        })
      );
      scheduleRef.current?.();
    },
    [updateItems]
  );

  const skipOne = useCallback(
    (key: string) => {
      updateItem(key, (item) =>
        item.state === 'needs-decision'
          ? {
              ...item,
              state: 'skipped',
              retryEligible: false,
              error: undefined,
            }
          : item
      );
    },
    [updateItem]
  );

  const replaceAll = useCallback(
    (batchId: string) => {
      updateItems((current) =>
        current.map((item) => {
          if (
            item.batchId !== batchId ||
            item.state !== 'needs-decision' ||
            item.localDuplicate ||
            item.targetStatus === 'directory'
          ) {
            return item;
          }
          return {
            ...item,
            overwrite: item.targetStatus === 'conflict',
            state: globallyPausedRef.current ? 'paused' : 'queued',
            retryEligible: false,
            error: undefined,
          };
        })
      );
      scheduleRef.current?.();
    },
    [updateItems]
  );

  const skipAll = useCallback(
    (batchId: string) => {
      updateItems((current) =>
        current.map((item) =>
          item.batchId === batchId && item.state === 'needs-decision'
            ? {
                ...item,
                state: 'skipped',
                retryEligible: false,
                error: undefined,
              }
            : item
        )
      );
    },
    [updateItems]
  );

  const uploadOnce = useCallback(
    async (key: string, controller: AbortController) => {
      let item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item) return;

      let file = item.file;
      if (item.fileHandle) {
        const handleResult = await inspectHandleFile(item.fileHandle);
        if (handleResult.status === 'missing') {
          markLocalMissing(key);
          return;
        }
        if (handleResult.status === 'available') {
          file = handleResult.file;
        }
        if (file && !matchesFingerprint(file, item.fingerprint)) {
          markLocalMissing(key);
          return;
        }
      }
      if (!file) {
        updateItem(key, (current) => ({
          ...current,
          state: 'paused',
          error: '請重新選擇原始檔案以繼續上傳。',
        }));
        return;
      }

      let session = item.session;
      if (session) {
        session = await getUploadSession(session, controller.signal);
        if (session.status === 'expired') {
          updateItem(key, (current) => ({
            ...current,
            session,
            state: 'failed',
            retryEligible: false,
            error: 'Upload session expired',
          }));
          return;
        }
      } else {
        const createInput = {
          path: item.logicPath,
          size: item.fingerprint.size,
          contentType: item.contentType,
          overwrite: item.overwrite,
          targetVersion: item.overwrite ? item.targetVersion : undefined,
        };
        session = await createUpload(createInput);
      }

      if (!session) throw new Error('Upload session unavailable');
      let activeSession: UploadSession = session;

      item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || cancelledKeysRef.current.has(key)) {
        if (cancelledKeysRef.current.has(key)) {
          cleanupUploadSession(activeSession.id);
        }
        return;
      }
      updateItem(key, (current) => ({
        ...current,
        file,
        session: activeSession,
        uploadedBytes: activeSession.uploadedSize ?? current.uploadedBytes,
        progress: progressFor(
          activeSession.uploadedSize ?? current.uploadedBytes,
          current.fingerprint.size
        ),
      }));

      let uploadedSize = activeSession.uploadedSize ?? 0;
      const total = item.fingerprint.size;
      if (total === 0 && activeSession.status !== 'uploaded') {
        const result = await putUploadChunk(
          activeSession,
          file.slice(0, 0, item.contentType),
          0,
          0,
          () => undefined,
          controller.signal
        );
        uploadedSize = result.uploadedSize;
      }

      while (uploadedSize < total) {
        controller.signal.throwIfAborted();
        const { start, endExclusive } = nextChunkRange(uploadedSize, total);
        try {
          const result = await putUploadChunk(
            activeSession,
            file.slice(start, endExclusive, item.contentType),
            start,
            total,
            (uploaded) => queueProgress(key, uploaded),
            controller.signal
          );
          uploadedSize = result.uploadedSize;
        } catch (error) {
          if (!isOffsetConflict(error)) throw error;
          activeSession = await getUploadSession(
            activeSession,
            controller.signal
          );
          uploadedSize = activeSession.uploadedSize ?? uploadedSize;
        }
        updateItem(key, (current) => ({
          ...current,
          session: { ...activeSession, uploadedSize },
          uploadedBytes: uploadedSize,
          progress: progressFor(uploadedSize, total),
        }));
      }

      const completedSession = await completeUpload(
        { ...activeSession, uploadedSize },
        controller.signal
      );
      const completedItem = itemsRef.current.find(
        (candidate) => candidate.key === key
      );
      if (!completedItem || cancelledKeysRef.current.has(key)) return;
      const nextItem: UploadQueueItem = {
        ...completedItem,
        file,
        session: completedSession,
        state: 'complete',
        uploadedBytes: total,
        progress: 100,
        retryCount: 0,
        retryEligible: false,
        retryAt: undefined,
        error: undefined,
      };
      updateItem(key, () => nextItem);
      onItemCompleteRef.current?.(nextItem);
    },
    [cleanupUploadSession, markLocalMissing, queueProgress, updateItem]
  );

  const startUpload = useCallback(
    async (key: string) => {
      const initial = itemsRef.current.find((item) => item.key === key);
      if (!initial || initial.state !== 'queued' || globallyPausedRef.current) {
        runningKeysRef.current.delete(key);
        return;
      }
      const controller = new AbortController();
      abortControllersRef.current.set(key, controller);
      updateItem(key, (item) => ({
        ...item,
        state: 'uploading',
        error: undefined,
        retryAt: undefined,
      }));

      let retryCount = initial.retryCount;
      try {
        for (;;) {
          try {
            await uploadOnce(key, controller);
            return;
          } catch (error) {
            if (
              cancelledKeysRef.current.has(key) ||
              pauseRequestedKeysRef.current.has(key) ||
              controller.signal.aborted
            ) {
              return;
            }
            if (isUploadTargetChanged(error)) {
              updateItem(key, (item) => ({
                ...item,
                state: 'checking',
                overwrite: false,
                targetVersion: undefined,
                targetStatus: undefined,
                existingTarget: undefined,
                retryCount: 0,
                retryEligible: false,
                retryAt: undefined,
                error: undefined,
              }));
              await runPreflight([key]);
              return;
            }
            if (shouldAutomaticallyRetry(error, retryCount)) {
              retryCount += 1;
              const delay = retryDelayMs(retryCount);
              updateItem(key, (item) => ({
                ...item,
                state: 'retrying',
                retryCount,
                retryEligible: true,
                retryAt: Date.now() + delay,
                error: errorMessage(error),
              }));
              await waitForRetry(delay, controller.signal);
              updateItem(key, (item) => ({
                ...item,
                state: 'uploading',
                retryAt: undefined,
                error: undefined,
              }));
              continue;
            }
            updateItem(key, (item) => ({
              ...item,
              state: 'failed',
              retryCount,
              retryEligible: isTransientUploadError(error),
              retryAt: undefined,
              error: errorMessage(error),
              progress: progressFor(item.uploadedBytes, item.fingerprint.size),
            }));
            return;
          }
        }
      } finally {
        runningKeysRef.current.delete(key);
        abortControllersRef.current.delete(key);
        cancelledKeysRef.current.delete(key);
        pauseRequestedKeysRef.current.delete(key);
        scheduleRef.current?.();
      }
    },
    [runPreflight, updateItem, uploadOnce]
  );

  const schedule = useCallback(() => {
    if (
      !mountedRef.current ||
      !hydratedRef.current ||
      globallyPausedRef.current
    )
      return;
    const available = MAX_CONCURRENT_UPLOADS - runningKeysRef.current.size;
    if (available <= 0) return;
    const nextKeys = nextRunnableUploadKeys(
      itemsRef.current,
      runningKeysRef.current,
      available
    );
    for (const key of nextKeys) {
      runningKeysRef.current.add(key);
      void startUpload(key);
    }
  }, [startUpload]);

  scheduleRef.current = schedule;

  useEffect(() => {
    schedule();
  }, [items, hydrated, globallyPaused, schedule]);

  const add = useCallback(
    (
      candidates: UploadCandidate[],
      destinationPath: string,
      existingNames: Set<string>
    ) => {
      // Kept for API compatibility; authoritative conflict checks now come
      // from the full-path server preflight rather than the visible file list.
      void existingNames;
      const snappedDestination = normalizePath(destinationPath);
      const batchId = makeBatchId();
      const drafts = candidates.map((candidate) => {
        const relativePath = candidate.relativePath;
        const logicPath = normalizePath(
          [snappedDestination, relativePath].filter(Boolean).join('/')
        );
        return { candidate, relativePath, logicPath };
      });
      const duplicatePaths = duplicateLogicPaths(drafts);
      const additions: UploadQueueItem[] = drafts.map(
        ({ candidate, relativePath, logicPath }) => {
          const fingerprint = fileFingerprint(candidate.file);
          return {
            key: makeQueueKey(candidate),
            batchId,
            file: candidate.file,
            fileHandle: candidate.fileHandle,
            fingerprint,
            contentType: candidate.file.type || 'application/octet-stream',
            relativePath,
            destinationPath: snappedDestination,
            logicPath,
            uploadedBytes: 0,
            progress: 0,
            state: 'checking',
            overwrite: false,
            localDuplicate: duplicatePaths.has(logicPath),
            archiveGroupId: candidate.archiveGroupId,
            retryCount: 0,
            retryEligible: false,
          };
        }
      );
      if (additions.length === 0) return;
      void navigator.storage?.persist?.().catch(() => undefined);
      updateItems((current) => [...current, ...additions]);
      void runPreflight(additions.map((item) => item.key));
    },
    [runPreflight, updateItems]
  );

  const checkSources = useCallback(async () => {
    const candidates = itemsRef.current.filter(
      (item): item is UploadQueueItem & { fileHandle: FileSystemFileHandle } =>
        Boolean(item.fileHandle) &&
        !['complete', 'skipped', 'local-missing'].includes(item.state)
    );
    await Promise.all(
      candidates.map(async (item) => {
        try {
          const result = await inspectHandleFile(item.fileHandle);
          if (
            result.status === 'missing' ||
            (result.status === 'available' &&
              !matchesFingerprint(result.file, item.fingerprint))
          ) {
            markLocalMissing(item.key);
          }
        } catch {
          markLocalMissing(item.key);
        }
      })
    );
  }, [markLocalMissing]);

  const summary = useMemo<UploadQueueSummary>(() => {
    const counts: UploadQueueSummary = {
      total: items.length,
      queued: 0,
      checking: 0,
      needsDecision: 0,
      skipped: 0,
      uploading: 0,
      retrying: 0,
      paused: 0,
      complete: 0,
      failed: 0,
      localMissing: 0,
      totalBytes: 0,
      uploadedBytes: 0,
      progress: 0,
    };
    let progressBytes = 0;
    for (const item of items) {
      if (item.state === 'local-missing') counts.localMissing += 1;
      else if (item.state === 'needs-decision') counts.needsDecision += 1;
      else counts[item.state] += 1;
      counts.totalBytes += item.fingerprint.size;
      counts.uploadedBytes += Math.min(
        item.fingerprint.size,
        item.uploadedBytes
      );
      progressBytes +=
        item.state === 'skipped'
          ? item.fingerprint.size
          : Math.min(item.fingerprint.size, item.uploadedBytes);
    }
    counts.progress =
      counts.totalBytes > 0
        ? Math.min(100, (progressBytes / counts.totalBytes) * 100)
        : counts.total > 0
        ? ((counts.complete + counts.skipped) / counts.total) * 100
        : 0;
    return counts;
  }, [items]);

  const hasPendingUploads = items.some((item) =>
    ['checking', 'queued', 'uploading', 'retrying'].includes(item.state)
  );

  useEffect(() => {
    mountedRef.current = true;
    void loadUploadQueue()
      .then(async ({ items: storedItems, globallyPaused: storedPaused }) => {
        const restored = await Promise.all(
          storedItems.map(async (stored): Promise<UploadQueueItem> => {
            const { sourceFile, ...persisted } = stored;
            let file =
              sourceFile && matchesFingerprint(sourceFile, stored.fingerprint)
                ? sourceFile
                : undefined;
            let sourceMissing = false;
            let permissionRequired = false;
            if (stored.fileHandle) {
              try {
                const result = await inspectHandleFile(stored.fileHandle);
                if (result.status === 'available') {
                  if (matchesFingerprint(result.file, stored.fingerprint)) {
                    file = result.file;
                  } else {
                    sourceMissing = true;
                  }
                } else if (result.status === 'missing') {
                  sourceMissing = true;
                } else {
                  permissionRequired = true;
                }
              } catch {
                sourceMissing = true;
              }
            }
            let state: UploadQueueState = stored.state;
            let error = stored.error;
            let missingFromState = stored.missingFromState;
            if (sourceMissing) {
              missingFromState =
                stored.state === 'local-missing'
                  ? stored.missingFromState
                  : stored.state;
              state = 'local-missing';
              error = '已從本機移除';
            } else if (
              !file &&
              !stored.fileHandle &&
              uploadStateNeedsSource(stored.state)
            ) {
              state = 'paused';
              error = '請重新選擇原始檔案以繼續上傳。';
            } else if (
              !file &&
              permissionRequired &&
              uploadStateNeedsSource(stored.state)
            ) {
              state = 'paused';
              error = '請允許讀取原始檔案以繼續上傳。';
            } else if (
              !stored.session &&
              !stored.targetStatus &&
              ['queued', 'uploading', 'retrying'].includes(stored.state)
            ) {
              state = 'checking';
              error = undefined;
            } else if (
              ['queued', 'uploading', 'retrying'].includes(stored.state)
            ) {
              state = storedPaused ? 'paused' : 'queued';
            }
            return {
              ...persisted,
              batchId: persisted.batchId || persisted.key,
              localDuplicate: persisted.localDuplicate ?? false,
              file,
              state,
              error,
              missingFromState,
              progress: progressFor(
                stored.uploadedBytes,
                stored.fingerprint.size
              ),
            };
          })
        );
        if (!mountedRef.current) return;
        itemsRef.current = restored;
        globallyPausedRef.current = storedPaused;
        setItems(restored);
        setGloballyPaused(storedPaused);
        hydratedRef.current = true;
        setHydrated(true);
        persist(restored, storedPaused);
        const checkingKeys = restored
          .filter((item) => item.state === 'checking')
          .map((item) => item.key);
        if (checkingKeys.length > 0) void runPreflight(checkingKeys);
      })
      .catch(() => {
        hydratedRef.current = true;
        setHydrated(true);
      });
    return () => {
      mountedRef.current = false;
    };
  }, [persist, runPreflight]);

  useEffect(() => {
    if (!hydrated) return;
    const handleFocus = () => void checkSources();
    window.addEventListener('focus', handleFocus);
    const interval = window.setInterval(handleFocus, SOURCE_CHECK_INTERVAL_MS);
    void checkSources();
    return () => {
      window.removeEventListener('focus', handleFocus);
      window.clearInterval(interval);
    };
  }, [checkSources, hydrated]);

  useEffect(() => {
    if (!hasPendingUploads) return;
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', warnBeforeUnload);
    return () => window.removeEventListener('beforeunload', warnBeforeUnload);
  }, [hasPendingUploads]);

  useEffect(
    () => () => {
      if (
        progressFrameRef.current !== undefined &&
        typeof window !== 'undefined'
      ) {
        window.cancelAnimationFrame(progressFrameRef.current);
      }
      for (const controller of abortControllersRef.current.values()) {
        controller.abort();
      }
      abortControllersRef.current.clear();
    },
    []
  );

  return {
    items,
    add,
    retry,
    retryAll,
    replaceOne,
    skipOne,
    replaceAll,
    skipAll,
    dismiss,
    cancel,
    cancelAll,
    pause,
    resume,
    pauseAll,
    resumeAll,
    reconnect,
    authorizeSource,
    globallyPaused,
    hydrated,
    hasPendingUploads,
    summary,
  };
}

type UploadQueue = ReturnType<typeof useUploadQueue>;

const UploadQueueContext = createContext<UploadQueue | null>(null);

/** Keeps uploads alive while the user navigates between browser routes. */
export function UploadQueueProvider({ children }: { children: ReactNode }) {
  const queue = useUploadQueue();
  return createElement(UploadQueueContext.Provider, { value: queue }, children);
}

export function useBackgroundUploadQueue() {
  const queue = useContext(UploadQueueContext);
  if (!queue) {
    throw new Error(
      'useBackgroundUploadQueue must be used inside UploadQueueProvider'
    );
  }
  return queue;
}
