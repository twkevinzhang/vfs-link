import { renderToStaticMarkup } from 'react-dom/server';
import type { ComponentProps } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { UploadActivity } from './upload-activity';
import type { UploadQueueItem } from './upload-queue';
import { summarizeUploadQueue } from '../domain/upload-queue';

function uploadItem(
  key: string,
  state: UploadQueueItem['state']
): UploadQueueItem {
  return {
    key,
    batchId: 'batch-1',
    sourceId: `source-${key}`,
    fingerprint: {
      name: key,
      size: 10,
      lastModified: 1,
    },
    contentType: 'application/octet-stream',
    relativePath: key,
    destinationPath: '',
    logicPath: key,
    uploadedBytes: state === 'complete' ? 10 : 0,
    progress: state === 'complete' ? 100 : 0,
    state,
    overwrite: false,
    localDuplicate: false,
    retryCount: 0,
    retryEligible: false,
  };
}

function uploadActivityMarkup(items: UploadQueueItem[]) {
  const action = vi.fn();
  const queue: ComponentProps<typeof UploadActivity>['queue'] = {
    items,
    summary: summarizeUploadQueue(items),
    globallyPaused: false,
    cancel: action,
    clearFinished: action,
    dismiss: action,
    pause: action,
    pauseAll: action,
    replaceAll: action,
    replaceOne: action,
    resume: action,
    resumeAll: action,
    retry: action,
    retryAll: action,
    skipAll: action,
    skipOne: action,
  };

  return renderToStaticMarkup(
    <UploadActivity
      queue={queue}
      expanded
      onExpandedChange={action}
      onRequestCancelAll={action}
    />
  );
}

describe('UploadActivity', () => {
  it('shows Clear all only when the queue has finished tasks', () => {
    expect(
      uploadActivityMarkup([uploadItem('done.txt', 'complete')])
    ).toContain('Clear all');
    expect(
      uploadActivityMarkup([uploadItem('active.txt', 'uploading')])
    ).not.toContain('Clear all');
  });

  it('renders uploading tasks before the remaining queue', () => {
    const markup = uploadActivityMarkup([
      uploadItem('queued.txt', 'queued'),
      uploadItem('uploading.txt', 'uploading'),
      uploadItem('done.txt', 'complete'),
    ]);

    expect(markup.indexOf('uploading.txt')).toBeLessThan(
      markup.indexOf('queued.txt')
    );
  });
});
