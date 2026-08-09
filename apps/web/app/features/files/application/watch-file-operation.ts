import type { FileOperationResponse } from '../domain/files';

const DEFAULT_INTERVAL_MS = 1_500;
const DEFAULT_DEADLINE_MS = 15 * 60 * 1_000;

export class FileOperationPollingTimeoutError extends Error {
  constructor(deadlineMs: number) {
    super(`Background operation monitoring timed out after ${deadlineMs}ms`);
    this.name = 'FileOperationPollingTimeoutError';
  }
}

type WatchFileOperationOptions = {
  id: string;
  fetchOperation: (
    id: string,
    signal: AbortSignal
  ) => Promise<FileOperationResponse>;
  onUpdate?: (operation: FileOperationResponse) => void;
  signal?: AbortSignal;
  intervalMs?: number;
  deadlineMs?: number;
};

function abortError(signal: AbortSignal) {
  return signal.reason instanceof Error
    ? signal.reason
    : new DOMException('Aborted', 'AbortError');
}

function wait(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const timer = globalThis.setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      globalThis.clearTimeout(timer);
      reject(abortError(signal));
    };
    if (signal.aborted) {
      onAbort();
      return;
    }
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

export async function watchFileOperation({
  id,
  fetchOperation,
  onUpdate,
  signal,
  intervalMs = DEFAULT_INTERVAL_MS,
  deadlineMs = DEFAULT_DEADLINE_MS,
}: WatchFileOperationOptions): Promise<FileOperationResponse> {
  const controller = new AbortController();
  let deadlineReached = false;
  const onExternalAbort = () => controller.abort(signal?.reason);
  if (signal?.aborted) {
    onExternalAbort();
  } else {
    signal?.addEventListener('abort', onExternalAbort, { once: true });
  }
  const deadline = globalThis.setTimeout(() => {
    deadlineReached = true;
    controller.abort();
  }, deadlineMs);

  try {
    for (;;) {
      let operation: FileOperationResponse;
      try {
        operation = await fetchOperation(id, controller.signal);
      } catch (error) {
        if (deadlineReached) {
          throw new FileOperationPollingTimeoutError(deadlineMs);
        }
        if (controller.signal.aborted) {
          throw abortError(controller.signal);
        }
        throw error;
      }
      if (deadlineReached) {
        throw new FileOperationPollingTimeoutError(deadlineMs);
      }
      if (controller.signal.aborted) {
        throw abortError(controller.signal);
      }
      onUpdate?.(operation);
      if (operation.status === 'completed' || operation.status === 'failed') {
        return operation;
      }
      try {
        await wait(intervalMs, controller.signal);
      } catch (error) {
        if (deadlineReached) {
          throw new FileOperationPollingTimeoutError(deadlineMs);
        }
        throw error;
      }
    }
  } finally {
    globalThis.clearTimeout(deadline);
    signal?.removeEventListener('abort', onExternalAbort);
  }
}
