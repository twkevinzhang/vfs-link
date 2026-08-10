import { describe, expect, it, vi } from 'vitest';

import { UploadQueueController } from './upload-queue-controller';
import type { UploadQueueDependencies } from './upload-queue-dependencies';
import { UploadQueueService } from './upload-queue-service';
import type {
  UploadPreflightItemInput,
  UploadSession,
} from './upload-contracts';

function uploadSession(overrides: Partial<UploadSession> = {}): UploadSession {
  return {
    id: 'session-1',
    status: 'uploading',
    uploadedSize: 0,
    expiresAt: '2099-01-01T00:00:00Z',
    ...overrides,
  };
}

function dependencies(): UploadQueueDependencies {
  return {
    gateway: {
      preflightUploads: vi.fn(async (items: UploadPreflightItemInput[]) => ({
        items: items.map((item) => ({
          ...item,
          status: 'available' as const,
          targetVersion: 'v1',
        })),
      })),
      createUpload: vi.fn(async () => uploadSession()),
      getUploadSession: vi.fn(async () => uploadSession()),
      putUploadChunk: vi.fn(
        async (_session, _sourceId, _start, endExclusive) => ({
          uploadedSize: endExclusive,
          status: 200,
        })
      ),
      completeUpload: vi.fn(async (session) => ({
        ...session,
        status: 'complete' as const,
      })),
      cancelUpload: vi.fn(async () => undefined),
    },
    errors: {
      isOffsetConflict: () => false,
      isTargetChanged: () => false,
      isTransient: () => false,
      shouldAutomaticallyRetry: () => false,
    },
    sources: {
      register: vi.fn(() => ({
        sourceId: 'unused',
        name: 'unused',
        size: 0,
        lastModified: 0,
        contentType: 'application/octet-stream',
      })),
      release: vi.fn(),
      clear: vi.fn(),
    },
    thumbnails: {
      save: vi.fn(async () => undefined),
      clear: vi.fn(async () => undefined),
    },
    runtime: {
      now: () => 0,
      sleep: vi.fn(async () => undefined),
      scheduleFrame: (callback) => {
        queueMicrotask(callback);
        return () => undefined;
      },
    },
  };
}

describe('UploadQueueController', () => {
  it('orchestrates preflight, chunk upload, and completion without React', async () => {
    const ports = dependencies();
    const controller = new UploadQueueController(ports);
    controller.add(
      [
        {
          sourceId: 'source-1',
          name: 'hello.txt',
          size: 5,
          lastModified: 1,
          contentType: 'text/plain',
          relativePath: 'hello.txt',
        },
      ],
      ''
    );

    await vi.waitFor(() => {
      expect(controller.getSnapshot().items[0].state).toBe('complete');
    });
    expect(ports.gateway.preflightUploads).toHaveBeenCalledOnce();
    expect(ports.gateway.putUploadChunk).toHaveBeenCalledOnce();
    expect(ports.sources.release).toHaveBeenCalledWith('source-1');
    controller.dispose();
  });

  it('replaceOne releases every newly skipped peer and its archive thumbnail', async () => {
    const ports = dependencies();
    const controller = new UploadQueueController(ports);
    controller.addArchives(
      [
        {
          id: 'archive-1',
          paths: ['same.zip'],
          thumbnail: { sourceId: 'thumbnail-1', width: 10, height: 10 },
          sources: [
            {
              sourceId: 'source-first',
              name: 'same.zip',
              size: 5,
              lastModified: 1,
              contentType: 'application/zip',
              relativePath: 'same.zip',
              archiveGroupId: 'archive-1',
            },
            {
              sourceId: 'source-second',
              name: 'same.zip',
              size: 5,
              lastModified: 1,
              contentType: 'application/zip',
              relativePath: 'same.zip',
              archiveGroupId: 'archive-1',
            },
          ],
        },
      ],
      ''
    );
    await vi.waitFor(() => {
      expect(
        controller
          .getSnapshot()
          .items.every((item) => item.state === 'needs-decision')
      ).toBe(true);
    });
    const selected = controller.getSnapshot().items[1];
    controller.replaceOne(selected.key);

    expect(ports.sources.release).toHaveBeenCalledWith('source-first');
    expect(ports.sources.release).toHaveBeenCalledWith('thumbnail-1');
    controller.dispose();
  });

  it('skipOne and skipAll release every newly skipped source', async () => {
    const ports = dependencies();
    ports.gateway.preflightUploads = vi.fn(
      async (items: UploadPreflightItemInput[]) => ({
        items: items.map((item) => ({
          ...item,
          status: 'conflict' as const,
          targetVersion: 'v1',
        })),
      })
    );
    const controller = new UploadQueueController(ports);
    controller.add(
      [
        {
          sourceId: 'source-one',
          name: 'one.txt',
          size: 1,
          lastModified: 1,
          contentType: 'text/plain',
          relativePath: 'one.txt',
        },
        {
          sourceId: 'source-two',
          name: 'two.txt',
          size: 1,
          lastModified: 1,
          contentType: 'text/plain',
          relativePath: 'two.txt',
        },
      ],
      ''
    );
    await vi.waitFor(() => {
      expect(
        controller
          .getSnapshot()
          .items.every((item) => item.state === 'needs-decision')
      ).toBe(true);
    });
    const [first] = controller.getSnapshot().items;
    controller.skipOne(first.key);
    controller.skipAll(first.batchId);

    expect(ports.sources.release).toHaveBeenCalledWith('source-one');
    expect(ports.sources.release).toHaveBeenCalledWith('source-two');
    controller.dispose();
  });

  it('clears finished items, releases their sources, and only cancels unfinished failed sessions', async () => {
    const ports = dependencies();
    ports.gateway.preflightUploads = vi.fn(
      async (items: UploadPreflightItemInput[]) => ({
        items: items.map((item) => ({
          ...item,
          status: 'conflict' as const,
          targetVersion: 'v1',
        })),
      })
    );
    const service = new UploadQueueService();
    const controller = new UploadQueueController(ports, service);
    controller.addArchives(
      [
        {
          id: 'archive-failed',
          paths: ['failed.zip'],
          thumbnail: { sourceId: 'thumbnail-failed', width: 10, height: 10 },
          sources: [
            {
              sourceId: 'source-failed',
              name: 'failed.zip',
              size: 5,
              lastModified: 1,
              contentType: 'application/zip',
              relativePath: 'failed.zip',
              archiveGroupId: 'archive-failed',
            },
          ],
        },
      ],
      ''
    );
    controller.add(
      [
        {
          sourceId: 'source-complete',
          name: 'complete.txt',
          size: 5,
          lastModified: 1,
          contentType: 'text/plain',
          relativePath: 'complete.txt',
        },
        {
          sourceId: 'source-skipped',
          name: 'skipped.txt',
          size: 5,
          lastModified: 1,
          contentType: 'text/plain',
          relativePath: 'skipped.txt',
        },
        {
          sourceId: 'source-failed-complete-session',
          name: 'failed-complete-session.txt',
          size: 5,
          lastModified: 1,
          contentType: 'text/plain',
          relativePath: 'failed-complete-session.txt',
        },
        {
          sourceId: 'source-active',
          name: 'active.txt',
          size: 5,
          lastModified: 1,
          contentType: 'text/plain',
          relativePath: 'active.txt',
        },
      ],
      ''
    );
    await vi.waitFor(() => {
      expect(
        controller
          .getSnapshot()
          .items.every((item) => item.state === 'needs-decision')
      ).toBe(true);
    });
    const bySourceId = new Map(
      service.getSnapshot().items.map((item) => [item.sourceId, item])
    );
    const failed = bySourceId.get('source-failed');
    const complete = bySourceId.get('source-complete');
    const skipped = bySourceId.get('source-skipped');
    const failedCompleteSession = bySourceId.get(
      'source-failed-complete-session'
    );
    if (!failed || !complete || !skipped || !failedCompleteSession) {
      throw new Error('Expected all upload queue fixtures');
    }
    service.setSession(
      failed.key,
      uploadSession({ id: 'failed-session', status: 'uploading' })
    );
    service.markFailed(failed.key, 'Upload failed', false);
    service.markComplete(
      complete.key,
      uploadSession({ id: 'complete-session', status: 'uploading' })
    );
    service.setSession(
      skipped.key,
      uploadSession({ id: 'skipped-session', status: 'uploading' })
    );
    service.skipOne(skipped.key);
    service.setSession(
      failedCompleteSession.key,
      uploadSession({ id: 'already-complete-session', status: 'complete' })
    );
    service.markFailed(
      failedCompleteSession.key,
      'Final response was lost',
      false
    );

    controller.clearFinished();

    expect(controller.getSnapshot().items).toHaveLength(1);
    expect(controller.getSnapshot().items[0]).toMatchObject({
      sourceId: 'source-active',
      state: 'needs-decision',
    });
    expect(ports.sources.release).toHaveBeenCalledWith('source-failed');
    expect(ports.sources.release).toHaveBeenCalledWith('source-complete');
    expect(ports.sources.release).toHaveBeenCalledWith('source-skipped');
    expect(ports.sources.release).toHaveBeenCalledWith(
      'source-failed-complete-session'
    );
    expect(ports.sources.release).toHaveBeenCalledWith('thumbnail-failed');
    expect(ports.sources.release).not.toHaveBeenCalledWith('source-active');
    expect(ports.gateway.cancelUpload).toHaveBeenCalledOnce();
    expect(ports.gateway.cancelUpload).toHaveBeenCalledWith('failed-session');
    controller.dispose();
  });

  it('coalesces 100 progress ticks into one immutable copy and one full publish per frame', async () => {
    const ports = dependencies();
    let frame: (() => void) | undefined;
    ports.runtime.scheduleFrame = vi.fn((callback) => {
      frame = callback;
      return () => {
        frame = undefined;
      };
    });
    let finishChunk: (() => void) | undefined;
    ports.gateway.putUploadChunk = vi.fn(
      async (_session, _sourceId, _start, endExclusive, _total, onProgress) => {
        for (let uploaded = 1; uploaded <= 100; uploaded += 1) {
          onProgress(uploaded, 100);
        }
        await new Promise<void>((resolve) => {
          finishChunk = resolve;
        });
        return { uploadedSize: endExclusive, status: 200 };
      }
    );
    const onItemsCopied = vi.fn();
    const service = new UploadQueueService(undefined, onItemsCopied);
    const fullListener = vi.fn();
    const structureListener = vi.fn();
    service.subscribe(fullListener);
    service.subscribeStructure(structureListener);
    const controller = new UploadQueueController(ports, service);
    controller.add(
      [
        {
          sourceId: 'progress-source',
          name: 'progress.bin',
          size: 100,
          lastModified: 1,
          contentType: 'application/octet-stream',
          relativePath: 'progress.bin',
        },
      ],
      ''
    );
    await vi.waitFor(() => expect(frame).toBeTypeOf('function'));
    const copiesBeforeFrame = onItemsCopied.mock.calls.length;
    const fullPublishesBeforeFrame = fullListener.mock.calls.length;
    const structurePublishesBeforeFrame = structureListener.mock.calls.length;

    frame?.();

    expect(onItemsCopied).toHaveBeenCalledTimes(copiesBeforeFrame + 1);
    expect(fullListener).toHaveBeenCalledTimes(fullPublishesBeforeFrame + 1);
    expect(structureListener).toHaveBeenCalledTimes(
      structurePublishesBeforeFrame
    );
    finishChunk?.();
    await vi.waitFor(() =>
      expect(controller.getSnapshot().items[0].state).toBe('complete')
    );
    controller.dispose();
  });
});
