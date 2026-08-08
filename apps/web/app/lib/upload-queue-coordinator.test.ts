import { describe, expect, it, vi } from 'vitest';

import { holdUploadQueueLeadership } from './upload-queue-coordinator';

describe('upload queue leadership', () => {
  it('uses the current tab as leader when Web Locks is unavailable', async () => {
    const controller = new AbortController();
    const states: string[] = [];
    const leadership = holdUploadQueueLeadership({
      signal: controller.signal,
      onState: (state) => states.push(state),
    });

    await Promise.resolve();
    expect(states).toEqual(['leader']);
    controller.abort();
    await leadership;
    expect(states).toEqual(['leader', 'stopped']);
  });

  it('waits for and releases an exclusive Web Lock', async () => {
    const controller = new AbortController();
    const states: string[] = [];
    const request = vi.fn(
      async (
        _name: string,
        _options: { mode: 'exclusive'; signal: AbortSignal },
        callback: (lock: Lock | null) => Promise<void>
      ) => callback({ name: 'upload', mode: 'exclusive' } as Lock)
    );
    const leadership = holdUploadQueueLeadership({
      locks: { request },
      signal: controller.signal,
      onState: (state) => states.push(state),
    });

    await Promise.resolve();
    expect(states).toEqual(['waiting', 'leader']);
    controller.abort();
    await leadership;
    expect(request).toHaveBeenCalledOnce();
    expect(states).toEqual(['waiting', 'leader', 'stopped']);
  });
});
