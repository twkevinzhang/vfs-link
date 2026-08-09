import type { UploadGateway } from './upload-gateway';

export type ArchiveTemporaryFileUsage = {
  name: string;
  size: number;
  lastModified: number;
  ownerId?: string;
};

export type ArchiveTemporaryStorageUsage = {
  files: ArchiveTemporaryFileUsage[];
  fileCount: number;
  totalBytes: number;
};

export type UploadQueueDependencies = {
  gateway: UploadGateway;
  errors: {
    isOffsetConflict(error: unknown): boolean;
    isTransient(error: unknown): boolean;
    isTargetChanged(error: unknown): boolean;
    shouldAutomaticallyRetry(
      error: unknown,
      retriesAlreadyUsed: number
    ): boolean;
  };
  archiveTemporaryStorage: {
    findOrphans(
      files: ArchiveTemporaryFileUsage[],
      manifests: unknown[],
      olderThan: number
    ): string[];
    listUsage(): Promise<ArchiveTemporaryStorageUsage>;
    remove(names: string[]): Promise<void>;
  };
};
