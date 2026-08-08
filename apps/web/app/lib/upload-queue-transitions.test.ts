import { describe, expect, it } from 'vitest';

import type { UploadQueueItem } from './upload-queue-model';
import {
  cancelUploadQueueItems,
  pauseUploadQueueItem,
  resumeUploadQueueItem,
} from './upload-queue-transitions';

function queueItem(overrides: Partial<UploadQueueItem> = {}): UploadQueueItem {
  return {
    key: 'upload-1',
    batchId: 'batch-1',
    fingerprint: { name: 'source.txt', size: 100, lastModified: 123 },
    contentType: 'text/plain',
    relativePath: 'source.txt',
    destinationPath: '',
    logicPath: 'source.txt',
    uploadedBytes: 25,
    progress: 7,
    state: 'queued',
    overwrite: false,
    localDuplicate: false,
    retryCount: 2,
    retryEligible: true,
    retryAt: 456,
    error: 'stale error',
    ...overrides,
  };
}

describe('upload queue public transitions', () => {
  it.each(['queued', 'uploading', 'retrying'] as const)(
    'pauses %s work and reconciles transient fields',
    (state) => {
      const paused = pauseUploadQueueItem(queueItem({ state }));

      expect(paused).toMatchObject({
        state: 'paused',
        progress: 25,
        error: undefined,
        retryAt: undefined,
      });
    }
  );

  it('does not pause an ineligible state', () => {
    const complete = queueItem({ state: 'complete', progress: 100 });
    expect(pauseUploadQueueItem(complete)).toBe(complete);
  });

  it('resumes paused work with a source without resetting progress or retry time', () => {
    const item = queueItem({
      state: 'paused',
      fileHandle: {} as FileSystemFileHandle,
      progress: 41,
      retryAt: 789,
    });

    expect(resumeUploadQueueItem(item)).toMatchObject({
      state: 'queued',
      progress: 41,
      retryAt: 789,
      error: undefined,
    });
  });

  it('keeps paused work unchanged when its local source is missing', () => {
    const missing = queueItem({ state: 'paused' });
    expect(resumeUploadQueueItem(missing)).toBe(missing);
    expect(resumeUploadQueueItem(missing)).toMatchObject({
      state: 'paused',
      progress: 7,
      retryAt: 456,
      error: 'stale error',
    });
  });

  it('cancels only the selected queue items', () => {
    const items = [
      queueItem({ key: 'keep', state: 'complete', progress: 100 }),
      queueItem({ key: 'cancel-running', state: 'uploading' }),
      queueItem({ key: 'cancel-paused', state: 'paused' }),
    ];

    expect(
      cancelUploadQueueItems(
        items,
        new Set(['cancel-running', 'cancel-paused'])
      ).map((item) => item.key)
    ).toEqual(['keep']);
  });
});
