import { afterEach, describe, expect, it, vi } from 'vitest';

import { getFileOperation, getShare } from './api';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('polling API cancellation', () => {
  it.each([
    ['file operation', () => getFileOperation('operation/1', { signal })],
    ['share', () => getShare('share/1', { signal })],
  ])('passes AbortSignal to the %s request', async (_label, request) => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    );

    await request();

    expect(fetchMock).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ signal })
    );
  });
});

const signal = new AbortController().signal;
