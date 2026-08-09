import { describe, expect, it, vi } from 'vitest';

import { UploadQueueService } from './upload-queue-service';
import { summarizeUploadQueue } from '../domain/upload-queue';

const source = (relativePath = 'photo.jpg') => ({
  sourceId: `source-${relativePath}`,
  name: relativePath.split('/').at(-1) ?? relativePath,
  size: 10,
  lastModified: 1,
  contentType: 'image/jpeg',
  relativePath,
});

describe('UploadQueueService', () => {
  it('owns session-only enqueue and server preflight transitions', () => {
    const service = new UploadQueueService();
    const listener = vi.fn();
    service.subscribe(listener);
    const [key] = service.add([source()], 'albums/2026');

    expect(service.getSnapshot().items[0]).toMatchObject({
      key,
      sourceId: 'source-photo.jpg',
      logicPath: 'albums/2026/photo.jpg',
      state: 'checking',
    });

    service.applyPreflight(new Set([key]), [
      {
        clientId: key,
        path: 'albums/2026/photo.jpg',
        status: 'available',
        targetVersion: 'v1',
      },
    ]);

    expect(service.getSnapshot().items[0].state).toBe('queued');
    expect(listener).toHaveBeenCalledTimes(2);
  });

  it('pauses and resumes the queue without persistence state', () => {
    const service = new UploadQueueService();
    const [key] = service.add([source()], '');
    service.applyPreflight(new Set([key]), [
      {
        clientId: key,
        path: 'photo.jpg',
        status: 'available',
        targetVersion: 'v1',
      },
    ]);

    service.pauseAll();
    expect(service.getSnapshot()).toMatchObject({ globallyPaused: true });
    expect(service.getSnapshot().items[0].state).toBe('paused');

    service.resumeAll();
    expect(service.getSnapshot()).toMatchObject({ globallyPaused: false });
    expect(service.getSnapshot().items[0].state).toBe('queued');
  });

  it('forces same-path selections through an explicit decision', () => {
    const service = new UploadQueueService();
    const keys = service.add(
      [source('same.txt'), { ...source('same.txt'), sourceId: 'second' }],
      ''
    );
    service.applyPreflight(
      new Set(keys),
      keys.map((key) => ({
        clientId: key,
        path: 'same.txt',
        status: 'available' as const,
        targetVersion: 'v1',
      }))
    );
    expect(service.getSnapshot().items.map((item) => item.state)).toEqual([
      'needs-decision',
      'needs-decision',
    ]);
    service.replaceOne(keys[1]);
    expect(service.getSnapshot().items.map((item) => item.state)).toEqual([
      'skipped',
      'queued',
    ]);
  });

  it('copies a 1,000-item snapshot once for a progress batch without structural notification', () => {
    const summarize = vi.fn(summarizeUploadQueue);
    const onItemsCopied = vi.fn();
    const service = new UploadQueueService(summarize, onItemsCopied);
    const sources = Array.from({ length: 1_000 }, (_, index) =>
      source(`file-${index}.bin`)
    );
    const keys = service.add(sources, 'bulk');
    const summaryCallsAfterAdd = summarize.mock.calls.length;
    const fullListener = vi.fn();
    const structureListener = vi.fn();
    service.subscribe(fullListener);
    service.subscribeStructure(structureListener);

    service.setProgressBatch(
      new Map(keys.slice(0, 100).map((key, index) => [key, index + 1]))
    );

    expect(summarize).toHaveBeenCalledTimes(summaryCallsAfterAdd);
    expect(onItemsCopied).toHaveBeenCalledOnce();
    expect(fullListener).toHaveBeenCalledOnce();
    expect(structureListener).not.toHaveBeenCalled();
  });
});
