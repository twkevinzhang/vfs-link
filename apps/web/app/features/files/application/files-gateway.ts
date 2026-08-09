import type {
  DeleteResponse,
  FileMutationResponse,
  FileOperationResponse,
  FilesResponse,
  StatusResponse,
  TrashResponse,
  TreeNode,
} from '../domain/files';

export type GetFilesOptions = {
  query?: string;
  limit?: number;
  offset?: number;
};

export type FilesGateway = {
  getStatus(): Promise<StatusResponse>;
  getFiles(path: string, options?: GetFilesOptions): Promise<FilesResponse>;
  getTree(path?: string): Promise<TreeNode>;
  moveFiles(
    paths: string[],
    destination: string
  ): Promise<FileMutationResponse | FileOperationResponse>;
  renameFile(
    path: string,
    name: string
  ): Promise<FileMutationResponse | FileOperationResponse>;
  getFileOperation(
    id: string,
    options?: { signal?: AbortSignal }
  ): Promise<FileOperationResponse>;
  moveFilesToTrash(
    paths: string[]
  ): Promise<FileMutationResponse | FileOperationResponse>;
  getTrash(): Promise<TrashResponse>;
  restoreTrash(
    trashIds: string[]
  ): Promise<FileMutationResponse | FileOperationResponse>;
  deleteTrash(
    trashIds: string[]
  ): Promise<DeleteResponse | FileOperationResponse>;
  emptyTrash(): Promise<DeleteResponse | FileOperationResponse>;
};
