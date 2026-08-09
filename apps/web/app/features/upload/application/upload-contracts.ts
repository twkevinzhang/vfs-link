import type { ArchiveTemporaryManifest } from '../domain/archive-manifest';

export type UploadSession = {
  id: string;
  logicPath: string;
  size: number;
  contentType: string;
  status:
    | 'pending'
    | 'uploading'
    | 'uploaded'
    | 'complete'
    | 'failed'
    | 'expired';
  uploadedSize: number;
  error?: string;
  method: 'PUT';
  uploadUrl: string;
  headers: Record<string, string>;
  completeUrl: string;
  statusUrl: string;
  expiresAt: string;
};

export type CreateUploadInput = {
  path: string;
  size: number;
  contentType: string;
  overwrite: boolean;
  targetVersion?: string;
};

export type UploadPreflightStatus = 'available' | 'conflict' | 'directory';
export type UploadPreflightItemInput = { clientId: string; path: string };
export type UploadPreflightExisting = {
  kind: 'file' | 'directory';
  size: number;
  updatedAt: string;
};
export type UploadPreflightItem = UploadPreflightItemInput & {
  status: UploadPreflightStatus;
  existing?: UploadPreflightExisting;
  targetVersion: string;
};
export type UploadPreflightResponse = { items: UploadPreflightItem[] };

/**
 * Transitional browser source contract. File and FileSystemHandle stay in the
 * application edge and are deliberately excluded from upload domain models.
 */
export type UploadCandidate = {
  file: File;
  fileHandle?: FileSystemFileHandle;
  sourceHandlePersistence?: 'durable' | 'non-durable';
  relativePath: string;
  selectionRoot: string;
  selectionRootKind: 'file' | 'directory';
  archiveGroupId?: string;
  archiveTemporaryManifest?: ArchiveTemporaryManifest;
};

export type PreparedArchiveBatch = {
  id: string;
  candidates: UploadCandidate[];
  thumbnail?: { blob: Blob; width: number; height: number };
  temporaryNames: string[];
  temporaryManifest?: ArchiveTemporaryManifest;
};

export type UploadDialogDependencies = {
  removeArchiveTemporaryFiles(names: string[]): Promise<void>;
};
