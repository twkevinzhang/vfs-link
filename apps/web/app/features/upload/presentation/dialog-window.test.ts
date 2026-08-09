import { describe, expect, it } from 'vitest';

import {
  INITIAL_DIALOG_ROW_LIMIT,
  mapWithConcurrency,
  nextDialogRowLimit,
  searchableThumbnailOptions,
  visibleDialogRows,
} from './dialog-window';

describe('upload dialog windowing', () => {
  it('limits a 1,000-item candidate or archive-plan render to 100 rows', () => {
    const items = Array.from({ length: 1_000 }, (_, index) => index);
    expect(visibleDialogRows(items, INITIAL_DIALOG_ROW_LIMIT)).toHaveLength(
      100
    );
    expect(nextDialogRowLimit(100, items.length)).toBe(200);
    expect(nextDialogRowLimit(950, items.length)).toBe(1_000);
  });

  it('keeps thumbnail preparation concurrency bounded and result order stable', async () => {
    const items = Array.from({ length: 1_000 }, (_, index) => index);
    let active = 0;
    let peak = 0;
    const results = await mapWithConcurrency(items, 4, async (item) => {
      active += 1;
      peak = Math.max(peak, active);
      await Promise.resolve();
      active -= 1;
      return item * 2;
    });

    expect(peak).toBeLessThanOrEqual(4);
    expect(results).toHaveLength(1_000);
    expect(results[999]).toBe(1_998);
  });

  it('finds and retains the 10,000th thumbnail while keeping DOM options bounded', () => {
    const candidates = Array.from({ length: 10_000 }, (_, index) => ({
      relativePath: `image-${index}.jpg`,
    }));
    const searchResults = searchableThumbnailOptions(
      candidates,
      'image-9999.jpg',
      'image-0.jpg'
    );
    expect(searchResults.length).toBeLessThanOrEqual(100);
    expect(
      searchResults.some((item) => item.relativePath === 'image-9999.jpg')
    ).toBe(true);

    const options = searchableThumbnailOptions(
      candidates,
      '',
      'image-9999.jpg'
    );

    expect(options).toHaveLength(100);
    expect(options.at(-1)?.relativePath).toBe('image-9999.jpg');
  });

  it('rejects immediately on abort without starting the remaining plans', async () => {
    const controller = new AbortController();
    let started = 0;
    const work = mapWithConcurrency(
      Array.from({ length: 10_000 }, (_, index) => index),
      4,
      async () => {
        started += 1;
        return new Promise<number>(() => undefined);
      },
      controller.signal
    );
    await Promise.resolve();
    expect(started).toBe(4);

    controller.abort();

    await expect(work).rejects.toMatchObject({ name: 'AbortError' });
    expect(started).toBe(4);
  });
});
