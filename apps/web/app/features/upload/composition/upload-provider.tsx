import type { ReactNode } from 'react';

import type { UploadQueueDependencies } from '../application/upload-queue-dependencies';
import {
  isOffsetConflict,
  isTransientUploadError,
  isUploadTargetChanged,
  shouldAutomaticallyRetry,
} from '../infrastructure/upload-error-mapping';
import { BrowserUploadSourceRegistry } from '../infrastructure/browser-upload-source-registry';
import { createUploadHttpGateway } from '../infrastructure/upload-http-gateway';
import { createUploadThumbnailGateway } from '../infrastructure/upload-thumbnail-gateway';
import { browserUploadRuntime } from '../infrastructure/browser-upload-runtime';
import { UploadQueueProvider } from '../presentation/upload-queue';

const sources = new BrowserUploadSourceRegistry();

const dependencies: UploadQueueDependencies = {
  gateway: createUploadHttpGateway(sources),
  errors: {
    isOffsetConflict,
    isTargetChanged: isUploadTargetChanged,
    isTransient: isTransientUploadError,
    shouldAutomaticallyRetry,
  },
  sources,
  thumbnails: createUploadThumbnailGateway(sources),
  runtime: browserUploadRuntime,
};

export function UploadProvider({ children }: { children: ReactNode }) {
  return (
    <UploadQueueProvider dependencies={dependencies}>
      {children}
    </UploadQueueProvider>
  );
}
