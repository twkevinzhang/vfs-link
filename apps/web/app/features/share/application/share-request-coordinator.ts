import { isTerminalShareStatus, type ShareRecord } from '../domain/share';
import type { ShareRequestCancellation } from './share-gateway';

const DEFAULT_DEADLINE_MS = 15_000;

export class ShareRequestTimeoutError extends Error {
  constructor(deadlineMs: number) {
    super(`Share request timed out after ${deadlineMs}ms`);
    this.name = 'ShareRequestTimeoutError';
  }
}

export { isTerminalShareStatus } from '../domain/share';

type ShareRequestCoordinatorOptions = {
  load: (cancellation: ShareRequestCancellation) => Promise<ShareRecord>;
  start?: (cancellation: ShareRequestCancellation) => Promise<ShareRecord>;
  onSuccess: (share: ShareRecord) => void;
  onError?: (error: unknown) => void;
  deadlineMs?: number;
  scheduler: ShareDeadlineScheduler;
};

export type ShareDeadlineScheduler = {
  setTimeout(callback: () => void, delayMs: number): unknown;
  clearTimeout(handle: unknown): void;
};

type CancellationSource = ShareRequestCancellation & { cancel(): void };

function createCancellationSource(): CancellationSource {
  let cancelled = false;
  const listeners = new Set<() => void>();
  return {
    get cancelled() {
      return cancelled;
    },
    onCancel(listener) {
      if (cancelled) listener();
      else listeners.add(listener);
      return () => listeners.delete(listener);
    },
    cancel() {
      if (cancelled) return;
      cancelled = true;
      for (const listener of listeners) listener();
      listeners.clear();
    },
  };
}

type ActiveRequest = {
  cancellation: CancellationSource;
  generation: number;
  promise: Promise<ShareRecord | undefined>;
};

export type ShareRequestCoordinator = {
  poll: () => Promise<ShareRecord | undefined>;
  refresh: () => Promise<ShareRecord | undefined>;
  start: () => Promise<ShareRecord | undefined>;
  cancel: () => void;
  dispose: () => void;
};

export function createShareRequestCoordinator({
  load,
  start,
  onSuccess,
  onError,
  deadlineMs = DEFAULT_DEADLINE_MS,
  scheduler,
}: ShareRequestCoordinatorOptions): ShareRequestCoordinator {
  let active: ActiveRequest | undefined;
  let disposed = false;
  let generation = 0;
  let terminal = false;

  const run = (
    request: (cancellation: ShareRequestCancellation) => Promise<ShareRecord>
  ) => {
    active?.cancellation.cancel();
    const cancellation = createCancellationSource();
    const requestGeneration = ++generation;
    let deadlineReached = false;
    const deadline = scheduler.setTimeout(() => {
      deadlineReached = true;
      cancellation.cancel();
    }, deadlineMs);

    const promise = (async (): Promise<ShareRecord | undefined> => {
      try {
        const nextShare = await request(cancellation);
        if (deadlineReached) {
          throw new ShareRequestTimeoutError(deadlineMs);
        }
        if (
          disposed ||
          cancellation.cancelled ||
          requestGeneration !== generation
        ) {
          return undefined;
        }
        terminal = isTerminalShareStatus(nextShare.status);
        onSuccess(nextShare);
        return nextShare;
      } catch (error) {
        if (
          disposed ||
          (cancellation.cancelled && !deadlineReached) ||
          requestGeneration !== generation
        ) {
          return undefined;
        }
        const reportedError = deadlineReached
          ? new ShareRequestTimeoutError(deadlineMs)
          : error;
        onError?.(reportedError);
        throw reportedError;
      } finally {
        scheduler.clearTimeout(deadline);
        if (active?.generation === requestGeneration) {
          active = undefined;
        }
      }
    })();
    active = { cancellation, generation: requestGeneration, promise };
    return promise;
  };

  return {
    poll() {
      if (disposed || terminal) {
        return Promise.resolve(undefined);
      }
      return active?.promise ?? run(load);
    },
    refresh() {
      if (disposed) {
        return Promise.resolve(undefined);
      }
      return run(load);
    },
    start() {
      if (disposed) {
        return Promise.resolve(undefined);
      }
      if (!start) {
        return Promise.reject(
          new Error('Share start operation is unavailable')
        );
      }
      terminal = false;
      return run(start);
    },
    cancel() {
      generation += 1;
      terminal = false;
      active?.cancellation.cancel();
      active = undefined;
    },
    dispose() {
      disposed = true;
      generation += 1;
      active?.cancellation.cancel();
      active = undefined;
    },
  };
}

/** Coordinator errors are already reflected through onError; UI fire-and-forget
 * handlers use this helper to prevent an expected rejection from escaping. */
export async function settleShareRequest(
  request: Promise<ShareRecord | undefined>
) {
  try {
    return await request;
  } catch {
    return undefined;
  }
}
