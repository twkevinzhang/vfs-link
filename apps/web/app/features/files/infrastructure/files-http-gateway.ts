import type {
  FilesGateway,
  GetFilesOptions,
} from '../application/files-gateway';
import type {
  DeleteResponse,
  FileMutationResponse,
  FileOperationResponse,
  FilesResponse,
  StatusResponse,
  TrashResponse,
  TreeNode,
} from '../domain/files';
import {
  API_BASE_URL,
  apiUrl,
  postJson,
  requestJson,
  type RequestOptions,
} from '../../../shared/infrastructure/http/http-client';

export function getStatus() {
  return requestJson<StatusResponse>('/api/status');
}

export function getFiles(path: string, options: GetFilesOptions = {}) {
  const query = new URLSearchParams({ path });
  const normalizedQuery = options.query?.trim();
  if (normalizedQuery) query.set('q', normalizedQuery);
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  if (options.offset !== undefined) query.set('offset', String(options.offset));
  return requestJson<FilesResponse>(`/api/files?${query.toString()}`);
}

export function getTree(path = '/') {
  return requestJson<TreeNode>(`/api/tree?${new URLSearchParams({ path })}`);
}

export function moveFiles(paths: string[], destination: string) {
  return postJson<FileMutationResponse | FileOperationResponse>(
    '/api/files/move',
    { paths, destination }
  );
}

export function renameFile(path: string, name: string) {
  return postJson<FileMutationResponse | FileOperationResponse>(
    '/api/files/rename',
    { path, name }
  );
}

export function getFileOperation(id: string, options: RequestOptions = {}) {
  return requestJson<FileOperationResponse>(
    `/api/operations/${encodeURIComponent(id)}`,
    options
  );
}

export function moveFilesToTrash(paths: string[]) {
  return postJson<FileMutationResponse | FileOperationResponse>(
    '/api/files/trash',
    { paths }
  );
}

export function getTrash() {
  return requestJson<TrashResponse>('/api/trash');
}

export function restoreTrash(trashIds: string[]) {
  return postJson<FileMutationResponse | FileOperationResponse>(
    '/api/trash/restore',
    { trashIds }
  );
}

export function deleteTrash(trashIds: string[]) {
  return postJson<DeleteResponse | FileOperationResponse>('/api/trash/delete', {
    trashIds,
  });
}

export function emptyTrash() {
  return postJson<DeleteResponse | FileOperationResponse>(
    '/api/trash/empty',
    {}
  );
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

export async function createThumbnail(input: {
  paths: string[];
  blob: Blob;
  width: number;
  height: number;
}) {
  const body = new FormData();
  body.set('paths', JSON.stringify(input.paths));
  body.set('width', String(input.width));
  body.set('height', String(input.height));
  body.set('thumbnail', input.blob, 'thumbnail.webp');
  const response = await fetch(apiUrl('/api/thumbnails'), {
    method: 'POST',
    headers: { Accept: 'application/json' },
    body,
  });
  if (!response.ok)
    throw new Error(`${response.status} ${response.statusText}`);
  return response.json() as Promise<{
    id: string;
    url: string;
    width: number;
    height: number;
  }>;
}

export async function deleteThumbnails(paths: string[]) {
  const response = await fetch(apiUrl('/api/thumbnails'), {
    method: 'DELETE',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ paths }),
  });
  if (!response.ok)
    throw new Error(`${response.status} ${response.statusText}`);
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
} satisfies FilesGateway;
