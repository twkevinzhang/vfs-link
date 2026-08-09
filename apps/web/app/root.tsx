import type { LinksFunction } from 'react-router';
import { Links, Meta, Outlet, Scripts, ScrollRestoration } from 'react-router';

import './app.css';
import type { UploadQueueDependencies } from './features/upload/application/upload-queue-dependencies';
import {
  isOffsetConflict,
  isTransientUploadError,
  isUploadTargetChanged,
  shouldAutomaticallyRetry,
} from './features/upload/infrastructure/upload-error-mapping';
import { uploadHttpGateway } from './features/upload/infrastructure/upload-http-gateway';
import { UploadQueueProvider } from './features/upload/presentation/upload-queue';
import {
  findArchiveTemporaryOrphanNames,
  listArchiveTemporaryStorageUsage,
  removeArchiveTemporaryFiles,
} from './lib/archive-temporary-storage';
import { appPath } from './lib/base-path';

const uploadQueueDependencies: UploadQueueDependencies = {
  gateway: uploadHttpGateway,
  errors: {
    isOffsetConflict,
    isTargetChanged: isUploadTargetChanged,
    isTransient: isTransientUploadError,
    shouldAutomaticallyRetry,
  },
  archiveTemporaryStorage: {
    findOrphans: findArchiveTemporaryOrphanNames,
    listUsage: listArchiveTemporaryStorageUsage,
    remove: removeArchiveTemporaryFiles,
  },
};

export const links: LinksFunction = () => [
  { rel: 'icon', type: 'image/svg+xml', href: appPath('/favicon.svg') },
];

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-Hant">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <Meta />
        <Links />
      </head>
      <body>
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export default function App() {
  return (
    <UploadQueueProvider dependencies={uploadQueueDependencies}>
      <Outlet />
    </UploadQueueProvider>
  );
}
