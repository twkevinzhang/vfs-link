import { describe, expect, it, vi } from 'vitest';

import { UploadHttpError } from './api';
import { isOffsetConflict } from '../features/upload/infrastructure/upload-error-mapping';
import { UPLOAD_CHUNK_SIZE } from './upload-queue-core';
import { uploadRemainingChunks } from './upload-chunks';

function virtualFile() {
  return {
    slice(start: number, end: number, type?: string) {
      return { size: end - start, type } as Blob;
    },
  };
}

describe('adaptive upload transport', () => {
  it('fills at least 90% of a 190 Mbps path with delayed acknowledgements', async () => {
    const linkBytesPerSecond = (190 * 1_000_000) / 8;
    const roundTripMs = 200;
    const totalSize = 2 * 1024 * 1024 * 1024;
    const chunkSizes: number[] = [];
    let elapsedMs = 0;

    const uploadedSize = await uploadRemainingChunks({
      file: virtualFile(),
      uploadedSize: 0,
      totalSize,
      contentType: 'application/octet-stream',
      signal: new AbortController().signal,
      sendChunk: async (chunk, start) => {
        chunkSizes.push(chunk.size);
        elapsedMs += (chunk.size / linkBytesPerSecond) * 1_000 + roundTripMs;
        return { uploadedSize: start + chunk.size, status: 308 };
      },
      reconcileOffset: vi.fn(),
      isOffsetConflict,
      onProgress: vi.fn(),
      onCommitted: vi.fn(),
      now: () => elapsedMs,
    });

    const achievedMbps = (uploadedSize * 8) / (elapsedMs / 1_000) / 1_000_000;
    expect(chunkSizes.slice(0, 2)).toEqual([8 * 1024 * 1024, 32 * 1024 * 1024]);
    expect(chunkSizes[2]).toBeGreaterThan(64 * 1024 * 1024);
    expect(achievedMbps).toBeGreaterThanOrEqual(190 * 0.9);
    expect(uploadedSize).toBe(totalSize);
  });

  it('reconciles a 409 before sending from the server offset', async () => {
    const totalSize = 48 * 1024 * 1024;
    const starts: number[] = [];
    let attempts = 0;

    await uploadRemainingChunks({
      file: virtualFile(),
      uploadedSize: 0,
      totalSize,
      contentType: 'application/octet-stream',
      signal: new AbortController().signal,
      sendChunk: async (chunk, start) => {
        attempts += 1;
        starts.push(start);
        if (attempts === 1) {
          throw new UploadHttpError('offset conflict', 409);
        }
        return { uploadedSize: start + chunk.size, status: 308 };
      },
      reconcileOffset: async () => 16 * 1024 * 1024,
      isOffsetConflict,
      onProgress: vi.fn(),
      onCommitted: vi.fn(),
      now: () => attempts * 1_000,
    });

    expect(starts.slice(0, 2)).toEqual([0, 16 * 1024 * 1024]);
  });

  it('stops before slicing another chunk after pause', async () => {
    const controller = new AbortController();
    controller.abort();
    const sendChunk = vi.fn();

    await expect(
      uploadRemainingChunks({
        file: virtualFile(),
        uploadedSize: 0,
        totalSize: UPLOAD_CHUNK_SIZE,
        contentType: 'application/octet-stream',
        signal: controller.signal,
        sendChunk,
        reconcileOffset: vi.fn(),
        isOffsetConflict,
        onProgress: vi.fn(),
        onCommitted: vi.fn(),
      })
    ).rejects.toHaveProperty('name', 'AbortError');
    expect(sendChunk).not.toHaveBeenCalled();
  });
});
