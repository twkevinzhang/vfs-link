import type { ComponentType } from 'react';

import type { FilesController } from '../application/files-controller';
import type { TreeNode } from '../domain/files';

export type FilesUploadQueueItem = {
  key: string;
  destinationPath: string;
  state: string;
};

export type FilesUploadQueue = {
  items: readonly FilesUploadQueueItem[];
  summary: {
    checking: number;
    queued: number;
    uploading: number;
    retrying: number;
    paused: number;
  };
  cancelAll(): void;
};

export type FilesUploadIntegration = {
  useQueue(): FilesUploadQueue;
  preloadDialog(): Promise<unknown>;
  Activity: ComponentType<{
    expanded: boolean;
    onExpandedChange(expanded: boolean): void;
    onRequestCancelAll(): void;
  }>;
  Dialog: ComponentType<{
    currentPath: string;
    open: boolean;
    onOpenChange(open: boolean): void;
  }>;
};

export type FilesPresentationDependencies = {
  loadTree(path?: string): Promise<TreeNode>;
  getDownloadUrl(path: string): string;
  getPreviewUrl(path: string): string;
  getThumbnailUrl(id: string): string;
};

/**
 * Cross-context behavior is supplied by the application composition root.
 */
export type FilesControllerDependencies = {
  controller: FilesController;
  getPreviewUrl(path: string): string;
  resolveAppPath(path: string): string;
  createShareDraft(path: string): Promise<{ id: string }>;
};
