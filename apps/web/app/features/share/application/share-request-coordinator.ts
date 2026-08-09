import { isTerminalShareStatus, type ShareRecord } from '../domain/share';

const DEFAULT_DEADLINE_MS = 15_000;

export class ShareRequestTimeoutError extends Error {
  constructor(deadlineMs: number) {
    super(`Share request timed out after ${deadlineMs}ms`);
    this.name = 'ShareRequestTimeoutError';
  }
}

export { isTerminalShareStatus } from '../domain/share';

type ShareRequestCoordinatorOptions = {
  load: (signal: AbortSignal) => Promise<ShareRecord>;
  start?: (signal: AbortSignal) => Promise<ShareRecord>;
  onSuccess: (share: ShareRecord) => void;
  onError?: (error: unknown) => void;
  deadlineMs?: number;
};

type ActiveRequest = {
  controller: AbortController;
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
}: ShareRequestCoordinatorOptions): ShareRequestCoordinator {
  let active: ActiveRequest | undefined;
  let disposed = false;
  let generation = 0;
  let terminal = false;

  const run = (request: (signal: AbortSignal) => Promise<ShareRecord>) => {
    active?.controller.abort();
    const controller = new AbortController();
    const requestGeneration = ++generation;
    let deadlineReached = false;
    const deadline = globalThis.setTimeout(() => {
      deadlineReached = true;
      controller.abort();
    }, deadlineMs);

    const promise = (async (): Promise<ShareRecord | undefined> => {
      try {
        const nextShare = await request(controller.signal);
        if (deadlineReached) {
          throw new ShareRequestTimeoutError(deadlineMs);
        }
        if (
          disposed ||
          controller.signal.aborted ||
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
          (controller.signal.aborted && !deadlineReached) ||
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
        globalThis.clearTimeout(deadline);
        if (active?.generation === requestGeneration) {
          active = undefined;
        }
      }
    })();
    active = { controller, generation: requestGeneration, promise };
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
      active?.controller.abort();
      active = undefined;
    },
    dispose() {
      disposed = true;
      generation += 1;
      active?.controller.abort();
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
