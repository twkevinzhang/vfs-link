import { describe, expect, it } from 'vitest';

import {
  INITIAL_UPLOAD_ROW_LIMIT,
  nextUploadRowLimit,
  visibleUploadRows,
} from './upload-list-window';

describe('upload list window', () => {
  it('caps the initial synchronous render for a 1000-item queue', () => {
    const items = Array.from({ length: 1_000 }, (_, index) => index);
    const visible = visibleUploadRows(items, INITIAL_UPLOAD_ROW_LIMIT);

    expect(visible).toHaveLength(100);
    expect(visible.at(-1)).toBe(99);
  });

  it('reveals one bounded page at a time without exceeding the queue', () => {
    expect(nextUploadRowLimit(100, 1_000)).toBe(200);
    expect(nextUploadRowLimit(950, 1_000)).toBe(1_000);
  });
});
