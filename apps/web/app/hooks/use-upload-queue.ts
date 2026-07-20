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
  putUpload,
} from '../lib/api';
import { normalizePath } from '../lib/format';
import type { UploadCandidate } from '../lib/folder-upload';
import type { UploadSession } from '../types/upload';

const MAX_CONCURRENT_UPLOADS = 3;
const COMPLETE_ITEM_LIFETIME_MS = 5_000;

export type UploadQueueItem = {
  key: string;
  file: File;
  relativePath: string;
  /** The folder selected when the item was added, rather than the current view. */
  destinationPath: string;
  /** Fully resolved storage path, also captured when the item was added. */
  logicPath: string;
  progress: number;
  state: 'queued' | 'uploading' | 'complete' | 'failed';
  error?: string;
  sessionId?: string;
  /** Whether a direct child with this name existed when the item was added. */
  overwrite: boolean;
};

type UploadQueueSummary = {
  total: number;
  queued: number;
  uploading: number;
  complete: number;
  failed: number;
  totalBytes: number;
  uploadedBytes: number;
  /** Byte-weighted progress across every retained queue item. */
  progress: number;
};

type UseUploadQueueOptions = {
  onItemComplete?: (item: UploadQueueItem) => void;
};

function makeQueueKey(candidate: UploadCandidate) {
  const randomId =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${candidate.relativePath}-${candidate.file.size}-${candidate.file.lastModified}-${randomId}`;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : 'Upload failed';
}

function isAlreadyExistsError(error: unknown) {
  return errorMessage(error).toLowerCase().includes('already exists');
}

function confirmOverwrite(relativePath: string) {
  return (
    typeof window !== 'undefined' &&
    window.confirm(`${relativePath} already exists. Replace it?`)
  );
}

/**
 * Keeps upload work alive above an UploadDialog. The browser must remain open,
 * but changing folders or closing the picker never interrupts queued uploads.
 */
export function useUploadQueue({ onItemComplete }: UseUploadQueueOptions = {}) {
  const [items, setItems] = useState<UploadQueueItem[]>([]);
  const itemsRef = useRef(items);
  const onItemCompleteRef = useRef(onItemComplete);
  const runningKeysRef = useRef(new Set<string>());
  const progressRef = useRef(new Map<string, number>());
  const progressFrameRef = useRef<number | undefined>(undefined);
  const completeTimersRef = useRef(
    new Map<string, ReturnType<typeof setTimeout>>()
  );
  const mountedRef = useRef(true);
  const scheduleRef = useRef<(() => void) | undefined>(undefined);

  // Assigning during render keeps asynchronous upload completions pointed at the
  // newest callback, including a completion that happens before effects flush.
  onItemCompleteRef.current = onItemComplete;

  const updateItems = useCallback(
    (update: (current: UploadQueueItem[]) => UploadQueueItem[]) => {
      if (!mountedRef.current) return;
      // Keep the scheduler's ref current immediately. React may batch the
      // state update, while an exceptionally fast upload can finish before
      // that render commits and would otherwise be scheduled a second time.
      itemsRef.current = update(itemsRef.current);
      setItems((current) => {
        const next = update(current);
        itemsRef.current = next;
        return next;
      });
    },
    []
  );

  const clearCompleteTimer = useCallback((key: string) => {
    const timer = completeTimersRef.current.get(key);
    if (timer !== undefined) {
      clearTimeout(timer);
      completeTimersRef.current.delete(key);
    }
  }, []);

  const dismiss = useCallback(
    (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      // Removing a pending item would make the request unobservable, while this
      // queue deliberately does not expose cancellation for active uploads.
      if (!item || item.state === 'queued' || item.state === 'uploading')
        return;
      clearCompleteTimer(key);
      updateItems((current) =>
        current.filter((candidate) => candidate.key !== key)
      );
    },
    [clearCompleteTimer, updateItems]
  );

  const flushProgress = useCallback(() => {
    progressFrameRef.current = undefined;
    const pending = progressRef.current;
    if (pending.size === 0) return;
    progressRef.current = new Map();
    updateItems((current) =>
      current.map((item) => {
        const progress = pending.get(item.key);
        // A late XMLHttpRequest progress event must never reduce a completed
        // item back below 100%.
        if (progress === undefined || item.state === 'complete') return item;
        return { ...item, progress };
      })
    );
  }, [updateItems]);

  const queueProgress = useCallback(
    (key: string, progress: number) => {
      progressRef.current.set(key, Math.max(0, Math.min(100, progress)));
      if (progressFrameRef.current !== undefined) return;
      if (typeof window === 'undefined') return;
      progressFrameRef.current = window.requestAnimationFrame(flushProgress);
    },
    [flushProgress]
  );

  const retry = useCallback(
    (key: string) => {
      clearCompleteTimer(key);
      progressRef.current.delete(key);
      updateItems((current) =>
        current.map((item) =>
          item.key === key && item.state === 'failed'
            ? {
                ...item,
                state: 'queued',
                progress: 0,
                error: undefined,
                sessionId: undefined,
              }
            : item
        )
      );
    },
    [clearCompleteTimer, updateItems]
  );

  const startUpload = useCallback(
    async (key: string) => {
      const item = itemsRef.current.find((candidate) => candidate.key === key);
      if (!item || item.state !== 'queued') {
        runningKeysRef.current.delete(key);
        return;
      }

      updateItems((current) =>
        current.map((candidate) =>
          candidate.key === key
            ? {
                ...candidate,
                state: 'uploading',
                progress: 0,
                error: undefined,
              }
            : candidate
        )
      );

      let sessionId: string | undefined;
      try {
        let overwrite = item.overwrite;
        const createInput = {
          path: item.logicPath,
          size: item.file.size,
          contentType: item.file.type || 'application/octet-stream',
          overwrite: item.overwrite,
        };
        let session: UploadSession;
        try {
          session = await createUpload(createInput);
        } catch (error) {
          if (
            !overwrite &&
            isAlreadyExistsError(error) &&
            confirmOverwrite(item.relativePath)
          ) {
            overwrite = true;
            updateItems((current) =>
              current.map((candidate) =>
                candidate.key === key
                  ? { ...candidate, overwrite: true }
                  : candidate
              )
            );
            session = await createUpload({ ...createInput, overwrite: true });
          } else {
            throw error;
          }
        }
        sessionId = session.id;
        updateItems((current) =>
          current.map((candidate) =>
            candidate.key === key ? { ...candidate, sessionId } : candidate
          )
        );

        await putUpload(session, item.file, (uploaded, total) => {
          queueProgress(key, total > 0 ? (uploaded / total) * 100 : 0);
        });
        await completeUpload(session);

        progressRef.current.delete(key);
        const completedItem: UploadQueueItem = {
          ...item,
          sessionId,
          overwrite,
          state: 'complete',
          progress: 100,
          error: undefined,
        };
        updateItems((current) =>
          current.map((candidate) =>
            candidate.key === key ? completedItem : candidate
          )
        );
        if (mountedRef.current) onItemCompleteRef.current?.(completedItem);

        const timer = setTimeout(() => dismiss(key), COMPLETE_ITEM_LIFETIME_MS);
        completeTimersRef.current.set(key, timer);
      } catch (error) {
        progressRef.current.delete(key);
        updateItems((current) =>
          current.map((candidate) =>
            candidate.key === key
              ? {
                  ...candidate,
                  state: 'failed',
                  error: errorMessage(error),
                  sessionId,
                }
              : candidate
          )
        );
        if (sessionId) {
          void cancelUpload(sessionId).catch(() => undefined);
        }
      } finally {
        runningKeysRef.current.delete(key);
        scheduleRef.current?.();
      }
    },
    [dismiss, queueProgress, updateItems]
  );

  const schedule = useCallback(() => {
    const available = MAX_CONCURRENT_UPLOADS - runningKeysRef.current.size;
    if (available <= 0) return;

    const nextItems = itemsRef.current
      .filter(
        (item) =>
          item.state === 'queued' && !runningKeysRef.current.has(item.key)
      )
      .slice(0, available);

    for (const item of nextItems) {
      runningKeysRef.current.add(item.key);
      void startUpload(item.key);
    }
  }, [startUpload]);

  scheduleRef.current = schedule;

  useEffect(() => {
    schedule();
  }, [items, schedule]);

  const add = useCallback(
    (
      candidates: UploadCandidate[],
      destinationPath: string,
      existingNames: Set<string>
    ) => {
      const snappedDestination = normalizePath(destinationPath);
      const snappedNames = new Set(existingNames);
      const additions: UploadQueueItem[] = candidates.map((candidate) => {
        const relativePath = candidate.relativePath;
        const existingDirectFile =
          !relativePath.includes('/') && snappedNames.has(relativePath);
        const overwrite = existingDirectFile && confirmOverwrite(relativePath);
        return {
          key: makeQueueKey(candidate),
          file: candidate.file,
          relativePath,
          destinationPath: snappedDestination,
          logicPath: normalizePath(
            `${
              snappedDestination === '/' ? '' : snappedDestination
            }/${relativePath}`
          ),
          progress: 0,
          state: existingDirectFile && !overwrite ? 'failed' : 'queued',
          error:
            existingDirectFile && !overwrite
              ? 'Existing file was not replaced'
              : undefined,
          overwrite,
        };
      });
      if (additions.length === 0) return;
      updateItems((current) => [...current, ...additions]);
    },
    [updateItems]
  );

  const hasPendingUploads = items.some(
    (item) => item.state === 'queued' || item.state === 'uploading'
  );

  const summary = useMemo<UploadQueueSummary>(() => {
    const counts = {
      total: items.length,
      queued: 0,
      uploading: 0,
      complete: 0,
      failed: 0,
      totalBytes: 0,
      uploadedBytes: 0,
      progress: 0,
    };
    for (const item of items) {
      counts[item.state] += 1;
      counts.totalBytes += item.file.size;
      counts.uploadedBytes += item.file.size * (item.progress / 100);
    }
    counts.progress =
      counts.totalBytes > 0
        ? Math.min(100, (counts.uploadedBytes / counts.totalBytes) * 100)
        : counts.total > 0
        ? (counts.complete / counts.total) * 100
        : 0;
    return counts;
  }, [items]);

  useEffect(() => {
    if (!hasPendingUploads) return;
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', warnBeforeUnload);
    return () => window.removeEventListener('beforeunload', warnBeforeUnload);
  }, [hasPendingUploads]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (
        progressFrameRef.current !== undefined &&
        typeof window !== 'undefined'
      ) {
        window.cancelAnimationFrame(progressFrameRef.current);
      }
      for (const timer of completeTimersRef.current.values())
        clearTimeout(timer);
      completeTimersRef.current.clear();
      progressRef.current.clear();
    };
  }, []);

  return {
    items,
    add,
    retry,
    dismiss,
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
