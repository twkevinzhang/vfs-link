import { afterEach, describe, expect, it, vi } from 'vitest';

import type { FileOperationResult } from './files-results';
import {
  FileOperationCancelledError,
  FileOperationPollingTimeoutError,
  watchFileOperation,
} from './watch-file-operation';

function operation(status: FileOperationResult['status']): FileOperationResult {
  return {
    operationId: 'operation-1',
    type: 'move',
    status,
    progress: status === 'completed' ? 1 : 0,
    total: 1,
    createdAt: '2026-08-09T00:00:00Z',
    updatedAt: '2026-08-09T00:00:00Z',
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe('watchFileOperation', () => {
  it('stops requesting after the first terminal response', async () => {
    vi.useFakeTimers();
    const fetchOperation = vi
      .fn<(id: string) => Promise<FileOperationResult>>()
      .mockResolvedValueOnce(operation('pending'))
      .mockResolvedValueOnce(operation('completed'));
    const updates: FileOperationResult[] = [];

    const result = watchFileOperation({
      id: 'operation-1',
      fetchOperation,
      onUpdate: (next) => updates.push(next),
      intervalMs: 1_500,
      deadlineMs: 10_000,
    });

    await vi.advanceTimersByTimeAsync(1_500);
    await expect(result).resolves.toMatchObject({ status: 'completed' });
    await vi.advanceTimersByTimeAsync(5_000);

    expect(fetchOperation).toHaveBeenCalledTimes(2);
    expect(updates.map((next) => next.status)).toEqual([
      'pending',
      'completed',
    ]);
    expect(fetchOperation).toHaveBeenNthCalledWith(1, 'operation-1');
  });

  it('stops before requesting when the application cancellation is set', async () => {
    const fetchOperation =
      vi.fn<(id: string) => Promise<FileOperationResult>>();

    const result = watchFileOperation({
      id: 'operation-1',
      fetchOperation,
      cancellation: { cancelled: true },
    });

    await expect(result).rejects.toBeInstanceOf(FileOperationCancelledError);
    expect(fetchOperation).not.toHaveBeenCalled();
  });

  it('enforces a hard deadline for an active request', async () => {
    vi.useFakeTimers();
    const fetchOperation = vi.fn(
      () => new Promise<FileOperationResult>(() => undefined)
    );

    const result = watchFileOperation({
      id: 'operation-1',
      fetchOperation,
      deadlineMs: 5_000,
    });
    const assertion = expect(result).rejects.toBeInstanceOf(
      FileOperationPollingTimeoutError
    );
    await vi.advanceTimersByTimeAsync(5_000);

    await assertion;
    expect(fetchOperation).toHaveBeenCalledOnce();
  });

  it('does not sleep beyond the deadline between polling requests', async () => {
    vi.useFakeTimers();
    const fetchOperation = vi.fn().mockResolvedValue(operation('pending'));
    const result = watchFileOperation({
      id: 'operation-1',
      fetchOperation,
      intervalMs: 1_500,
      deadlineMs: 1_000,
    });
    const assertion = expect(result).rejects.toBeInstanceOf(
      FileOperationPollingTimeoutError
    );

    await vi.advanceTimersByTimeAsync(1_000);

    await assertion;
    expect(fetchOperation).toHaveBeenCalledOnce();
  });
});
