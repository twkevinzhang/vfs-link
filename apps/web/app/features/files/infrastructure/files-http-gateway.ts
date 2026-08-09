import type { FilesPort, GetFilesOptions } from '../application/files-port';
import type { TreeNode } from '../domain/files';
import type {
  DeleteResult,
  FileMutationResult,
  FileOperationResult,
  FilesResult,
  StatusResult,
  TrashResult,
} from '../application/files-results';
import {
  API_BASE_URL,
  apiUrl,
  postJson,
  requestJson,
  type RequestOptions,
} from '../../../shared/infrastructure/http/http-client';

export function getStatus() {
  return requestJson<StatusResult>('/api/status');
}

export function getFiles(path: string, options: GetFilesOptions = {}) {
  const query = new URLSearchParams({ path });
  const normalizedQuery = options.query?.trim();
  if (normalizedQuery) query.set('q', normalizedQuery);
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  if (options.offset !== undefined) query.set('offset', String(options.offset));
  return requestJson<FilesResult>(`/api/files?${query.toString()}`);
}

export function getTree(path = '/') {
  return requestJson<TreeNode>(`/api/tree?${new URLSearchParams({ path })}`);
}

export function moveFiles(paths: string[], destination: string) {
  return postJson<FileMutationResult | FileOperationResult>('/api/files/move', {
    paths,
    destination,
  });
}

export function renameFile(path: string, name: string) {
  return postJson<FileMutationResult | FileOperationResult>(
    '/api/files/rename',
    { path, name }
  );
}

export function getFileOperation(id: string, options: RequestOptions = {}) {
  return requestJson<FileOperationResult>(
    `/api/operations/${encodeURIComponent(id)}`,
    options
  );
}

export function moveFilesToTrash(paths: string[]) {
  return postJson<FileMutationResult | FileOperationResult>(
    '/api/files/trash',
    { paths }
  );
}

export function getTrash() {
  return requestJson<TrashResult>('/api/trash');
}

export function restoreTrash(trashIds: string[]) {
  return postJson<FileMutationResult | FileOperationResult>(
    '/api/trash/restore',
    { trashIds }
  );
}

export function deleteTrash(trashIds: string[]) {
  return postJson<DeleteResult | FileOperationResult>('/api/trash/delete', {
    trashIds,
  });
}

export function emptyTrash() {
  return postJson<DeleteResult | FileOperationResult>('/api/trash/empty', {});
}

export function getDownloadUrl(path: string) {
  return apiUrl(`/api/download?${new URLSearchParams({ path })}`);
}

export function getPreviewUrl(path: string) {
  return apiUrl(
    `/api/download?${new URLSearchParams({ path, disposition: 'inline' })}`
  );
}

export function getThumbnailUrl(id: string) {
  return `${API_BASE_URL}/api/thumbnails/${encodeURIComponent(id)}`;
}

export const filesHttpGateway = {
  deleteTrash,
  emptyTrash,
  getFileOperation,
  getFiles,
  getStatus,
  getTrash,
  getTree,
  moveFiles,
  moveFilesToTrash,
  renameFile,
  restoreTrash,
} satisfies FilesPort;
