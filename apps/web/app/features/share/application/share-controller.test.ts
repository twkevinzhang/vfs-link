import { afterEach, describe, expect, it, vi } from 'vitest';

import type { ShareRecord } from '../domain/share';
import { ShareController, type ShareScheduler } from './share-controller';
import type { ShareGateway } from './share-gateway';

const scheduler: ShareScheduler = {
  setTimeout: (callback, delay) => globalThis.setTimeout(callback, delay),
  clearTimeout: (handle) => globalThis.clearTimeout(handle as number),
  setInterval: (callback, delay) => globalThis.setInterval(callback, delay),
  clearInterval: (handle) => globalThis.clearInterval(handle as number),
};

function share(status: ShareRecord['status']): ShareRecord {
  return {
    id: 'share-1',
    logicPath: 'docs/report.pdf',
    fileName: 'report.pdf',
    size: 42,
    destinationObject: 'shares/report.pdf',
    destinationUrl: 'gs://bucket/shares/report.pdf',
    shareUrl: 'https://example.test/report.pdf',
    email: '',
    notificationTarget: '',
    status,
    createdAt: '2026-08-09T00:00:00Z',
    updatedAt: '2026-08-09T00:00:00Z',
  };
}

function gateway(getShare = vi.fn().mockResolvedValue(share('uploading'))) {
  return {
    createShareDraft: vi.fn(),
    getShare,
    startShare: vi.fn().mockResolvedValue(share('completed')),
  } satisfies ShareGateway;
}

afterEach(() => vi.useRealTimers());

describe('ShareController', () => {
  it('owns initial loading and polling lifecycle', async () => {
    vi.useFakeTimers();
    const getShare = vi
      .fn()
      .mockResolvedValueOnce(share('uploading'))
      .mockResolvedValueOnce(share('completed'));
    const controller = new ShareController(
      'share-1',
      gateway(getShare),
      scheduler
    );
    controller.start();
    await Promise.resolve();
    await Promise.resolve();
    expect(controller.getSnapshot().share?.status).toBe('uploading');

    await vi.advanceTimersByTimeAsync(1_500);
    expect(controller.getSnapshot().share?.status).toBe('completed');
    expect(getShare).toHaveBeenCalledTimes(2);

    controller.dispose();
    await vi.advanceTimersByTimeAsync(3_000);
    expect(getShare).toHaveBeenCalledTimes(2);
  });

  it('reports a missing id without calling infrastructure', () => {
    const shareGateway = gateway();
    const controller = new ShareController(undefined, shareGateway, scheduler);
    controller.start();

    expect(controller.getSnapshot()).toMatchObject({
      loading: false,
      error: 'Missing share id',
    });
    expect(shareGateway.getShare).not.toHaveBeenCalled();
  });
});
