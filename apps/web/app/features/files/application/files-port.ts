import type { TreeNode } from '../domain/files';
import type {
  DeleteResult,
  FileMutationResult,
  FileOperationResult,
  FilesResult,
  StatusResult,
  TrashResult,
} from './files-results';

export type GetFilesOptions = {
  query?: string;
  limit?: number;
  offset?: number;
};

/**
 * Application-owned port for file storage. Infrastructure adapters implement
 * this contract; use cases never import HTTP or browser APIs.
 */
export type FilesPort = {
  getStatus(): Promise<StatusResult>;
  getFiles(path: string, options?: GetFilesOptions): Promise<FilesResult>;
  getTree(path?: string): Promise<TreeNode>;
  moveFiles(
    paths: string[],
    destination: string
  ): Promise<FileMutationResult | FileOperationResult>;
  renameFile(
    path: string,
    name: string
  ): Promise<FileMutationResult | FileOperationResult>;
  getFileOperation(id: string): Promise<FileOperationResult>;
  moveFilesToTrash(
    paths: string[]
  ): Promise<FileMutationResult | FileOperationResult>;
  getTrash(): Promise<TrashResult>;
  restoreTrash(
    trashIds: string[]
  ): Promise<FileMutationResult | FileOperationResult>;
  deleteTrash(trashIds: string[]): Promise<DeleteResult | FileOperationResult>;
  emptyTrash(): Promise<DeleteResult | FileOperationResult>;
};
