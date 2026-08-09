import { describe, expect, it, vi } from 'vitest';

import { uploadRemainingChunks } from './upload-chunks';
import type { UploadCancellation } from './upload-contracts';

const activeCancellation: UploadCancellation = {
  aborted: false,
  onAbort: () => () => undefined,
  throwIfAborted: () => undefined,
};

describe('uploadRemainingChunks', () => {
  it('uses the server committed offset as the only next cursor', async () => {
    const ranges: Array<[number, number]> = [];
    const committed = vi.fn();
    const uploaded = await uploadRemainingChunks({
      sourceId: 'source',
      uploadedSize: 0,
      totalSize: 10,
      cancellation: activeCancellation,
      sendChunk: async (start, end) => {
        ranges.push([start, end]);
        return { uploadedSize: end, status: 200 };
      },
      reconcileOffset: async () => 0,
      isOffsetConflict: () => false,
      onProgress: () => undefined,
      onCommitted: committed,
      now: vi.fn().mockReturnValueOnce(0).mockReturnValue(1),
    });

    expect(uploaded).toBe(10);
    expect(ranges).toEqual([[0, 10]]);
    expect(committed).toHaveBeenCalledWith(10);
  });

  it('reconciles an ambiguous offset conflict before sending again', async () => {
    let attempts = 0;
    const starts: number[] = [];
    const uploaded = await uploadRemainingChunks({
      sourceId: 'source',
      uploadedSize: 0,
      totalSize: 16 * 1024 * 1024,
      cancellation: activeCancellation,
      sendChunk: async (start, end) => {
        starts.push(start);
        attempts += 1;
        if (attempts === 1) throw new Error('offset');
        return { uploadedSize: end, status: 200 };
      },
      reconcileOffset: async () => 4 * 1024 * 1024,
      isOffsetConflict: () => true,
      onProgress: () => undefined,
      onCommitted: () => undefined,
      now: () => attempts,
    });

    expect(uploaded).toBe(16 * 1024 * 1024);
    expect(starts.slice(0, 2)).toEqual([0, 4 * 1024 * 1024]);
  });
});
