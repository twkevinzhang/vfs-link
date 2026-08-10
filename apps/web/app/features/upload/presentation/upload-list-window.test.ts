import { describe, expect, it } from 'vitest';

import {
  INITIAL_UPLOAD_ROW_LIMIT,
  nextUploadRowLimit,
  prioritizeUploadingRows,
  visibleUploadRows,
} from './upload-list-window';

describe('upload list window', () => {
  it('caps the initial synchronous render for a 1000-item queue', () => {
    const items = Array.from({ length: 1_000 }, (_, index) => ({
      id: index,
      state: 'queued',
    }));
    const visible = visibleUploadRows(items, INITIAL_UPLOAD_ROW_LIMIT);

    expect(visible).toHaveLength(100);
    expect(visible.at(-1)?.id).toBe(99);
  });

  it('stably places uploading rows before every other state', () => {
    const items = [
      { id: 'queued-a', state: 'queued' },
      { id: 'uploading-a', state: 'uploading' },
      { id: 'complete-a', state: 'complete' },
      { id: 'uploading-b', state: 'uploading' },
      { id: 'failed-a', state: 'failed' },
    ];

    expect(prioritizeUploadingRows(items).map((item) => item.id)).toEqual([
      'uploading-a',
      'uploading-b',
      'queued-a',
      'complete-a',
      'failed-a',
    ]);
    expect(items.map((item) => item.id)).toEqual([
      'queued-a',
      'uploading-a',
      'complete-a',
      'uploading-b',
      'failed-a',
    ]);
  });

  it('prioritizes uploading rows before applying the visible window', () => {
    const items = Array.from({ length: 101 }, (_, index) => ({
      id: index,
      state: index === 100 ? 'uploading' : 'queued',
    }));

    const visible = visibleUploadRows(items, INITIAL_UPLOAD_ROW_LIMIT);

    expect(visible).toHaveLength(100);
    expect(visible[0]?.id).toBe(100);
    expect(visible.slice(1).map((item) => item.id)).toEqual(
      Array.from({ length: 99 }, (_, index) => index)
    );
  });

  it('reveals one bounded page at a time without exceeding the queue', () => {
    expect(nextUploadRowLimit(100, 1_000)).toBe(200);
    expect(nextUploadRowLimit(950, 1_000)).toBe(1_000);
  });
});
