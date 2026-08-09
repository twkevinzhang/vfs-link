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

import { type UploadQueueDependencies } from '../features/upload/application/upload-queue-dependencies';
import { normalizePath } from '../lib/format';
import type { UploadCandidate } from '../lib/folder-upload';
import type { ArchiveTemporaryManifest } from '../features/upload/domain/archive-manifest';
import { uploadRemainingChunks } from '../lib/upload-chunks';
import {
  holdUploadQueueLeadership,
  type UploadQueueLeadershipState,
} from '../lib/upload-queue-coordinator';
import {
  MAX_CONCURRENT_UPLOADS,
  fileFingerprint,
  matchesFingerprint,
} from '../lib/upload-queue-core';
import {
  duplicateLogicPaths,
  isRetryAllEligible,
  nextRunnableUploadKeys,
  retryDelayMs,
} from '../features/upload/domain/upload-queue';
import type {
  UploadQueueItem,
  UploadQueueState,
} from '../lib/upload-queue-model';
import {
  summarizeUploadQueue,
  toPersistedUploadItem,
  uploadErrorMessage,
  uploadProgress,
  waitForUploadRetry,
} from '../lib/upload-queue-runtime';
import {
  inspectUploadSource,
  type PermissionAwareFileHandle,
} from '../lib/upload-queue-source';
import { loadUploadQueue, saveUploadQueue } from '../lib/upload-queue-storage';
import {
  cancelUploadQueueItems,
  pauseUploadQueueItem,
  resumeUploadQueueItem,
} from '../lib/upload-queue-transitions';
import type { UploadSession } from '../features/upload/application/upload-contracts';
import {
  createUploadQueueLeadershipHandler,
  hydrateUploadQueueLifecycle,
} from './upload-queue-lifecycle';

export type {
  UploadQueueItem,
  UploadQueueState,
} from '../lib/upload-queue-model';

const SOURCE_CHECK_INTERVAL_MS = 15_000;
const ARCHIVE_ORPHAN_GRACE_MS = 24 * 60 * 60 * 1000;
const UPLOAD_QUEUE_CHANNEL = 'vfs-link-upload-queue-v1';

type UseUploadQueueOptions = {
  dependencies: UploadQueueDependencies;
  onItemComplete?: (item: UploadQueueItem) => void;
};

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

/**
 * Persistent, chunked upload coordinator. Server-confirmed uploadedSize is the
 * only resume cursor, so an aborted or ambiguous request is always reconciled
 * before another chunk is sent.
 */
export function useUploadQueue({
  dependencies,
  onItemComplete,
}: UseUploadQueueOptions) {
  const {
    archiveTemporaryStorage: {
      findOrphans: findArchiveTemporaryOrphanNames,
      listUsage: listArchiveTemporaryStorageUsage,
      remove: removeArchiveTemporaryFiles,
    },
    errors: {
      isOffsetConflict,
      isTargetChanged: isUploadTargetChanged,
      isTransient: isTransientUploadError,
      shouldAutomaticallyRetry,
    },
    gateway: {
      cancelUpload,
      completeUpload,
      createUpload,
      getUploadSession,
      preflightUploads,
      putUploadChunk,
    },
  } = dependencies;
  const [items, setItems] = useState<UploadQueueItem[]>([]);
  const [hydrated, setHydrated] = useState(false);
  const [globallyPaused, setGloballyPaused] = useState(false);
  const [leadershipState, setLeadershipState] =
    useState<UploadQueueLeadershipState>('waiting');
  const itemsRef = useRef(items);
  const globallyPausedRef = useRef(globallyPaused);
  const isUploadLeaderRef = useRef(false);
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
  const queueChannelRef = useRef<BroadcastChannel | undefined>(undefined);
  const archiveManifestsRef = useRef(
    new Map<string, ArchiveTemporaryManifest>()
  );
  const orphanCleanupStartedRef = useRef(false);
  const scheduleRef = useRef<(() => void) | undefined>(undefined);
  const preflightRef = useRef<((keys: string[]) => Promise<void>) | undefined>(
    undefined
  );

  onItemCompleteRef.current = onItemComplete;
  globallyPausedRef.current = globallyPaused;

  const persist = useCallback(
    (nextItems = itemsRef.current, nextPaused = globallyPausedRef.current) => {
      if (!hydratedRef.current || !isUploadLeaderRef.current) return;
      const snapshot = nextItems.map(toPersistedUploadItem);
      persistenceRef.current = persistenceRef.current
        .catch(() => undefined)
        .then(() => saveUploadQueue(snapshot, nextPaused))
        .then(() => queueChannelRef.current?.postMessage({ type: 'changed' }));
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

  const cleanupUploadSession = useCallback(
    (sessionId: string) => {
      void cancelUpload(sessionId).catch(() => undefined);
    },
    [cancelUpload]
  );

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
              Math.min(
                100,
                uploadProgress(uploadedBytes, item.fingerprint.size)
              )
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
        progress: uploadProgress(
          current.uploadedBytes,
          current.fingerprint.size
        ),
      }));
    },
    [updateItem]
  );

  const pause = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || pauseUploadQueueItem(item) === item) return;
      pauseRequestedKeysRef.current.add(key);
      abortControllersRef.current.get(key)?.abort();
      updateItem(key, pauseUploadQueueItem);
    },
    [updateItem]
  );

  const resume = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || resumeUploadQueueItem(item) === item) return;
      if (globallyPausedRef.current) {
        globallyPausedRef.current = false;
        setGloballyPaused(false);
        persist(itemsRef.current, false);
      }
      pauseRequestedKeysRef.current.delete(key);
      updateItem(key, resumeUploadQueueItem);
      scheduleRef.current?.();
    },
    [persist, updateItem]
  );

  const pauseAll = useCallback(() => {
    globallyPausedRef.current = true;
    setGloballyPaused(true);
    for (const item of itemsRef.current) {
      if (pauseUploadQueueItem(item) !== item) {
        pauseRequestedKeysRef.current.add(item.key);
        abortControllersRef.current.get(item.key)?.abort();
      }
    }
    updateItems((current) => current.map(pauseUploadQueueItem));
    persist(itemsRef.current, true);
  }, [persist, updateItems]);

  const resumeAll = useCallback(() => {
    globallyPausedRef.current = false;
    setGloballyPaused(false);
    pauseRequestedKeysRef.current.clear();
    updateItems((current) => current.map(resumeUploadQueueItem));
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
      updateItems((current) => cancelUploadQueueItems(current, new Set([key])));
      if (!runningKeysRef.current.has(key)) {
        cancelledKeysRef.current.delete(key);
      }
      scheduleRef.current?.();
    },
    [cleanupUploadSession, updateItems]
  );

  const cancelAll = useCallback(() => {
    const cancellableStates = new Set<UploadQueueState>([
      'checking',
      'needs-decision',
      'queued',
      'uploading',
      'retrying',
      'paused',
    ]);
    const cancellable = itemsRef.current.filter((item) =>
      cancellableStates.has(item.state)
    );
    if (cancellable.length === 0) return;

    const keys = new Set(cancellable.map((item) => item.key));
    for (const item of cancellable) {
      cancelledKeysRef.current.add(item.key);
      abortControllersRef.current.get(item.key)?.abort();
      if (item.session) cleanupUploadSession(item.session.id);
      pendingProgressRef.current.delete(item.key);
    }
    updateItems((current) => cancelUploadQueueItems(current, keys));
    for (const key of keys) {
      if (!runningKeysRef.current.has(key))
        cancelledKeysRef.current.delete(key);
    }
    scheduleRef.current?.();
  }, [cleanupUploadSession, updateItems]);

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
      if (!isUploadLeaderRef.current) return;
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
                  error: uploadErrorMessage(error),
                }
              : item
          )
        );
      }
    },
    [isTransientUploadError, preflightUploads, updateItems]
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
        const handleResult = await inspectUploadSource(item.fileHandle);
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
        progress: uploadProgress(
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

      uploadedSize = await uploadRemainingChunks({
        file,
        uploadedSize,
        totalSize: total,
        contentType: item.contentType,
        signal: controller.signal,
        sendChunk: (chunk, start, chunkTotal, onProgress, signal) =>
          putUploadChunk(
            activeSession,
            chunk,
            start,
            chunkTotal,
            onProgress,
            signal
          ),
        reconcileOffset: async () => {
          activeSession = await getUploadSession(
            activeSession,
            controller.signal
          );
          return activeSession.uploadedSize ?? 0;
        },
        isOffsetConflict,
        onProgress: (uploaded) => queueProgress(key, uploaded),
        onCommitted: (committedSize) => {
          activeSession = { ...activeSession, uploadedSize: committedSize };
          updateItem(key, (current) => ({
            ...current,
            session: activeSession,
            uploadedBytes: committedSize,
            progress: uploadProgress(committedSize, total),
          }));
        },
      });

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
    [
      cleanupUploadSession,
      completeUpload,
      createUpload,
      getUploadSession,
      isOffsetConflict,
      markLocalMissing,
      putUploadChunk,
      queueProgress,
      updateItem,
    ]
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
                error: uploadErrorMessage(error),
              }));
              await waitForUploadRetry(delay, controller.signal);
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
              error: uploadErrorMessage(error),
              progress: uploadProgress(
                item.uploadedBytes,
                item.fingerprint.size
              ),
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
    [
      isTransientUploadError,
      isUploadTargetChanged,
      runPreflight,
      shouldAutomaticallyRetry,
      updateItem,
      uploadOnce,
    ]
  );

  const schedule = useCallback(() => {
    if (
      !mountedRef.current ||
      !hydratedRef.current ||
      !isUploadLeaderRef.current ||
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
      if (!isUploadLeaderRef.current) return;
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
            archiveTemporaryManifest: candidate.archiveTemporaryManifest,
            retryCount: 0,
            retryEligible: false,
          };
        }
      );
      if (additions.length === 0) return;
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
          const result = await inspectUploadSource(item.fileHandle);
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

  const summary = useMemo(() => summarizeUploadQueue(items), [items]);

  const hasPendingUploads = items.some((item) =>
    ['checking', 'queued', 'uploading', 'retrying'].includes(item.state)
  );

  useEffect(() => {
    mountedRef.current = true;
    void hydrateUploadQueueLifecycle({
      isMounted: () => mountedRef.current,
      apply: (restored, storedPaused) => {
        itemsRef.current = restored;
        globallyPausedRef.current = storedPaused;
        setItems(restored);
        setGloballyPaused(storedPaused);
      },
      markHydrated: () => {
        hydratedRef.current = true;
        setHydrated(true);
      },
      persist,
      preflight: (keys) => void runPreflight(keys),
    });
    return () => {
      mountedRef.current = false;
    };
  }, [persist, runPreflight]);

  useEffect(() => {
    const controller = new AbortController();
    const onState = createUploadQueueLeadershipHandler({
      isMounted: () => mountedRef.current,
      isHydrated: () => hydratedRef.current,
      items: () => itemsRef.current,
      setLeader: (isLeader) => {
        isUploadLeaderRef.current = isLeader;
      },
      setState: setLeadershipState,
      reload: () => window.location.reload(),
      schedule: () => scheduleRef.current?.(),
      preflight: (keys) => void preflightRef.current?.(keys),
    });
    void holdUploadQueueLeadership({
      locks: navigator.locks,
      signal: controller.signal,
      onState,
    }).catch(() => {
      isUploadLeaderRef.current = false;
      if (mountedRef.current) setLeadershipState('stopped');
    });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    if (typeof BroadcastChannel === 'undefined') return;
    const channel = new BroadcastChannel(UPLOAD_QUEUE_CHANNEL);
    queueChannelRef.current = channel;
    channel.onmessage = (event: MessageEvent<{ type?: string }>) => {
      if (event.data?.type !== 'changed' || isUploadLeaderRef.current) return;
      void loadUploadQueue()
        .then(({ items: storedItems, globallyPaused: storedPaused }) => {
          if (!mountedRef.current || isUploadLeaderRef.current) return;
          const mirrored = storedItems.map(
            (stored): UploadQueueItem => ({
              ...stored,
              batchId: stored.batchId || stored.key,
              localDuplicate: stored.localDuplicate ?? false,
              file: undefined,
              progress: uploadProgress(
                stored.uploadedBytes,
                stored.fingerprint.size
              ),
            })
          );
          itemsRef.current = mirrored;
          globallyPausedRef.current = storedPaused;
          setItems(mirrored);
          setGloballyPaused(storedPaused);
        })
        .catch(() => undefined);
    };
    return () => {
      if (queueChannelRef.current === channel) {
        queueChannelRef.current = undefined;
      }
      channel.close();
    };
  }, []);

  useEffect(() => {
    if (!hydrated) return;
    const currentManifests = new Map<string, ArchiveTemporaryManifest>();
    for (const item of items) {
      const manifest = item.archiveTemporaryManifest;
      if (manifest) currentManifests.set(manifest.ownerId, manifest);
    }
    for (const [ownerId, manifest] of currentManifests) {
      archiveManifestsRef.current.set(ownerId, manifest);
    }
    for (const [ownerId, manifest] of archiveManifestsRef.current) {
      const stillNeeded = items.some(
        (item) =>
          item.archiveTemporaryManifest?.ownerId === ownerId &&
          !['complete', 'skipped', 'local-missing'].includes(item.state)
      );
      if (stillNeeded) continue;
      archiveManifestsRef.current.delete(ownerId);
      void removeArchiveTemporaryFiles(manifest.files.map((file) => file.name));
    }
  }, [hydrated, items, removeArchiveTemporaryFiles]);

  useEffect(() => {
    if (!hydrated || orphanCleanupStartedRef.current) return;
    orphanCleanupStartedRef.current = true;
    const manifests = items
      .map((item) => item.archiveTemporaryManifest)
      .filter((manifest): manifest is ArchiveTemporaryManifest =>
        Boolean(manifest)
      );
    void listArchiveTemporaryStorageUsage()
      .then(async (usage) => {
        const orphanNames = findArchiveTemporaryOrphanNames(
          usage.files,
          manifests,
          Date.now() - ARCHIVE_ORPHAN_GRACE_MS
        );
        await removeArchiveTemporaryFiles(orphanNames);
      })
      .catch(() => undefined);
  }, [
    findArchiveTemporaryOrphanNames,
    hydrated,
    items,
    listArchiveTemporaryStorageUsage,
    removeArchiveTemporaryFiles,
  ]);

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
    isUploadLeader: leadershipState === 'leader',
    leadershipState,
  };
}

type UploadQueue = ReturnType<typeof useUploadQueue>;

const UploadQueueContext = createContext<UploadQueue | null>(null);

/** Keeps uploads alive while the user navigates between browser routes. */
export function UploadQueueProvider({
  children,
  dependencies,
}: {
  children: ReactNode;
  dependencies: UploadQueueDependencies;
}) {
  const queue = useUploadQueue({ dependencies });
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
