import type { FileOperationResult } from './files-results';

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
  fetchOperation: (id: string) => Promise<FileOperationResult>;
  onUpdate?: (operation: FileOperationResult) => void;
  cancellation?: FileOperationCancellation;
  intervalMs?: number;
  deadlineMs?: number;
};

export type FileOperationCancellation = {
  readonly cancelled: boolean;
};

export class FileOperationCancelledError extends Error {
  constructor() {
    super('Background operation monitoring was cancelled');
    this.name = 'FileOperationCancelledError';
  }
}

function wait(ms: number) {
  return new Promise<void>((resolve) => {
    globalThis.setTimeout(resolve, ms);
  });
}

function enforceDeadline<T>(
  work: Promise<T>,
  remainingMs: number,
  deadlineMs: number
) {
  return new Promise<T>((resolve, reject) => {
    const deadline = globalThis.setTimeout(
      () => reject(new FileOperationPollingTimeoutError(deadlineMs)),
      remainingMs
    );
    work.then(resolve, reject).finally(() => globalThis.clearTimeout(deadline));
  });
}

export async function watchFileOperation({
  id,
  fetchOperation,
  onUpdate,
  cancellation,
  intervalMs = DEFAULT_INTERVAL_MS,
  deadlineMs = DEFAULT_DEADLINE_MS,
}: WatchFileOperationOptions): Promise<FileOperationResult> {
  const startedAt = Date.now();

  for (;;) {
    if (cancellation?.cancelled) {
      throw new FileOperationCancelledError();
    }
    if (Date.now() - startedAt >= deadlineMs) {
      throw new FileOperationPollingTimeoutError(deadlineMs);
    }
    const remainingMs = deadlineMs - (Date.now() - startedAt);
    const operation = await enforceDeadline(
      fetchOperation(id),
      remainingMs,
      deadlineMs
    );
    if (cancellation?.cancelled) {
      throw new FileOperationCancelledError();
    }
    if (Date.now() - startedAt >= deadlineMs) {
      throw new FileOperationPollingTimeoutError(deadlineMs);
    }
    onUpdate?.(operation);
    if (operation.status === 'completed' || operation.status === 'failed') {
      return operation;
    }
    await wait(Math.min(intervalMs, deadlineMs - (Date.now() - startedAt)));
  }
}
