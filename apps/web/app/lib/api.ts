import {
  DeleteResponse,
  FileOperationResponse,
  FileMutationResponse,
  FilesResponse,
  StatusResponse,
  TrashResponse,
  TreeNode,
} from '../types/files';
import { ShareRecord } from '../types/share';
import { CreateUploadInput, UploadSession } from '../types/upload';

// An empty base keeps browser requests on the same origin as the Web UI.
// Set VITE_API_BASE_URL only for an intentionally separate API deployment.
const API_BASE_URL = (import.meta.env.VITE_API_BASE_URL || '').replace(
  /\/$/,
  ''
);

async function requestJson<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, {
    headers: { Accept: 'application/json' },
  });

  if (!response.ok) {
    const fallback = `${response.status} ${response.statusText}`;
    let message = fallback;
    try {
      const body = (await response.json()) as { error?: string };
      message = body.error || fallback;
    } catch {
      message = fallback;
    }
    throw new Error(message);
  }

  return response.json() as Promise<T>;
}

async function postJson<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const fallback = `${response.status} ${response.statusText}`;
    let message = fallback;
    try {
      const responseBody = (await response.json()) as { error?: string };
      message = responseBody.error || fallback;
    } catch {
      message = fallback;
    }
    throw new Error(message);
  }

  return response.json() as Promise<T>;
}

function apiUrl(pathOrUrl: string) {
  if (/^https?:\/\//i.test(pathOrUrl)) {
    return pathOrUrl;
  }
  return `${API_BASE_URL}${pathOrUrl.startsWith('/') ? '' : '/'}${pathOrUrl}`;
}

export function getStatus() {
  return requestJson<StatusResponse>('/api/status');
}

export type GetFilesOptions = {
  query?: string;
  limit?: number;
  offset?: number;
};

export function getFiles(path: string, options: GetFilesOptions = {}) {
  const query = new URLSearchParams({ path });
  const normalizedQuery = options.query?.trim();
  if (normalizedQuery) {
    query.set('q', normalizedQuery);
  }
  if (options.limit !== undefined) {
    query.set('limit', String(options.limit));
  }
  if (options.offset !== undefined) {
    query.set('offset', String(options.offset));
  }
  return requestJson<FilesResponse>(`/api/files?${query.toString()}`);
}

export function getTree(path = '/') {
  const query = new URLSearchParams({ path });
  return requestJson<TreeNode>(`/api/tree?${query.toString()}`);
}

export function moveFiles(paths: string[], destination: string) {
  return postJson<FileMutationResponse | FileOperationResponse>(
    '/api/files/move',
    {
      paths,
      destination,
    }
  );
}

export function getFileOperation(id: string) {
  return requestJson<FileOperationResponse>(
    `/api/operations/${encodeURIComponent(id)}`
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
  const query = new URLSearchParams({ path });
  return `${API_BASE_URL}/api/download?${query.toString()}`;
}

export function getPreviewUrl(path: string) {
  const query = new URLSearchParams({ path, disposition: 'inline' });
  return `${API_BASE_URL}/api/download?${query.toString()}`;
}

export function createShareDraft(path: string) {
  return postJson<ShareRecord>('/api/shares/drafts', { path });
}

export function getShare(id: string) {
  return requestJson<ShareRecord>(`/api/shares/${encodeURIComponent(id)}`);
}

export function startShare(id: string) {
  return postJson<ShareRecord>(
    `/api/shares/${encodeURIComponent(id)}/start`,
    {}
  );
}

export function createUpload(input: CreateUploadInput) {
  return postJson<UploadSession>('/api/uploads', input);
}

export function completeUpload(session: UploadSession) {
  return postJson<UploadSession>(session.completeUrl, {});
}

export function cancelUpload(id: string) {
  return fetch(`${API_BASE_URL}/api/uploads/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}

// Passing the File directly to XMLHttpRequest keeps large files out of JS
// memory while still exposing native upload progress events.
export function putUpload(
  session: UploadSession,
  file: File,
  onProgress: (uploaded: number, total: number) => void
) {
  return new Promise<void>((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open(session.method, apiUrl(session.uploadUrl));
    for (const [name, value] of Object.entries(session.headers)) {
      request.setRequestHeader(name, value);
    }
    if (!session.headers['Content-Type'] && file.type) {
      request.setRequestHeader('Content-Type', file.type);
    }
    request.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable) {
        onProgress(event.loaded, event.total);
      } else {
        onProgress(event.loaded, file.size);
      }
    });
    request.addEventListener('load', () => {
      if (request.status >= 200 && request.status < 300) {
        resolve();
        return;
      }
      reject(
        new Error(`Upload failed: ${request.status} ${request.statusText}`)
      );
    });
    request.addEventListener('error', () =>
      reject(new Error('Upload connection failed'))
    );
    request.addEventListener('abort', () =>
      reject(new Error('Upload cancelled'))
    );
    request.send(file);
  });
}
