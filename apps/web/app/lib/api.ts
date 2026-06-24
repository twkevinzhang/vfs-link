import { FilesResponse, StatusResponse, TreeNode } from '../types/files';

const API_BASE_URL = (
  import.meta.env.VITE_API_BASE_URL || 'http://localhost:8080'
).replace(/\/$/, '');

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

export function getStatus() {
  return requestJson<StatusResponse>('/api/status');
}

export function getFiles(path: string) {
  const query = new URLSearchParams({ path });
  return requestJson<FilesResponse>(`/api/files?${query.toString()}`);
}

export function getTree() {
  return requestJson<TreeNode>('/api/tree');
}

export function getDownloadUrl(path: string) {
  const query = new URLSearchParams({ path });
  return `${API_BASE_URL}/api/download?${query.toString()}`;
}
