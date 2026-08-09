import type { FilesGateway } from './files-gateway';
import type { TreeNode } from '../domain/files';

export type FilesControllerDependencies = FilesGateway & {
  getPreviewUrl(path: string): string;
  createThumbnail(input: {
    paths: string[];
    blob: Blob;
    width: number;
    height: number;
  }): Promise<unknown>;
  deleteThumbnails(paths: string[]): Promise<void>;
  createShareDraft(path: string): Promise<{ id: string }>;
  removeArchiveTemporaryFiles(names: string[]): Promise<void>;
};

/** Browser-facing URL and tree ports supplied by a route composition root. */
export type FilesPresentationDependencies = {
  loadTree(path?: string): Promise<TreeNode>;
  getDownloadUrl(path: string): string;
  getPreviewUrl(path: string): string;
  getThumbnailUrl(id: string): string;
};
