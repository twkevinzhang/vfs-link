import { afterEach, describe, expect, it, vi } from 'vitest';

import type { FileOperationResponse } from '../domain/files';
import {
  FileOperationPollingTimeoutError,
  watchFileOperation,
} from './watch-file-operation';

function operation(
  status: FileOperationResponse['status']
): FileOperationResponse {
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
      .fn<(id: string, signal: AbortSignal) => Promise<FileOperationResponse>>()
      .mockResolvedValueOnce(operation('pending'))
      .mockResolvedValueOnce(operation('completed'));
    const updates: FileOperationResponse[] = [];

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
    expect(fetchOperation.mock.calls[0]?.[1]).toBeInstanceOf(AbortSignal);
  });

  it('aborts an in-flight request when the caller is disposed', async () => {
    const controller = new AbortController();
    let receivedSignal: AbortSignal | undefined;
    const fetchOperation = vi.fn(
      (_id: string, signal: AbortSignal) =>
        new Promise<FileOperationResponse>((_resolve, reject) => {
          receivedSignal = signal;
          signal.addEventListener(
            'abort',
            () => reject(new DOMException('Aborted', 'AbortError')),
            { once: true }
          );
        })
    );

    const result = watchFileOperation({
      id: 'operation-1',
      fetchOperation,
      signal: controller.signal,
    });
    controller.abort();

    await expect(result).rejects.toMatchObject({ name: 'AbortError' });
    expect(receivedSignal?.aborted).toBe(true);
  });

  it('enforces a hard deadline and aborts the active request', async () => {
    vi.useFakeTimers();
    let receivedSignal: AbortSignal | undefined;
    const fetchOperation = vi.fn(
      (_id: string, signal: AbortSignal) =>
        new Promise<FileOperationResponse>((_resolve, reject) => {
          receivedSignal = signal;
          signal.addEventListener(
            'abort',
            () => reject(new DOMException('Aborted', 'AbortError')),
            { once: true }
          );
        })
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
    expect(receivedSignal?.aborted).toBe(true);
  });
});
