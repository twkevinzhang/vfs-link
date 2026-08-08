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
import {
  DriftAction,
  DriftActionsResponse,
  DriftCurrentScanResponse,
  DriftPlan,
  DriftResponse,
  DriftScan,
} from '../types/drift';

// An empty base keeps browser requests on the same origin as the Web UI.
// Set VITE_API_BASE_URL only for an intentionally separate API deployment.
const API_BASE_URL = (import.meta.env.VITE_API_BASE_URL || '').replace(
  /\/$/,
  ''
);

export class UploadHttpError extends Error {
  constructor(message: string, public readonly status?: number) {
    super(message);
    this.name = 'UploadHttpError';
  }
}

async function requestJson<T>(path: string): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, {
    headers: { Accept: 'application/json' },
  });

  if (!response.ok) {
    const fallback = `${response.status} ${response.statusText}`;
    let message = fallback;
    try {
      const body = (await response.json()) as {
        error?: string;
        snapshotStatus?: string;
      };
      message =
        body.error ||
        (body.snapshotStatus === 'missing' ? 'no drift snapshot' : fallback);
    } catch {
      message = fallback;
    }
    throw new UploadHttpError(message, response.status);
  }

  return response.json() as Promise<T>;
}

async function postJson<T>(
  path: string,
  body: unknown,
  options: { signal?: AbortSignal } = {}
): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
    signal: options.signal,
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
    throw new UploadHttpError(message, response.status);
  }

  return response.json() as Promise<T>;
}

async function deleteResource(path: string): Promise<void> {
  const response = await fetch(`${API_BASE_URL}${path}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
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

export function renameFile(path: string, name: string) {
  return postJson<FileMutationResponse | FileOperationResponse>(
    '/api/files/rename',
    { path, name }
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
  const response = await fetch(`${API_BASE_URL}/api/thumbnails`, {
    method: 'POST',
    headers: { Accept: 'application/json' },
    body,
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      message =
        ((await response.json()) as { error?: string }).error || message;
    } catch {
      // Use the HTTP fallback when the response is not JSON.
    }
    throw new Error(message);
  }
  return response.json() as Promise<{
    id: string;
    url: string;
    width: number;
    height: number;
  }>;
}

export async function deleteThumbnails(paths: string[]) {
  const response = await fetch(`${API_BASE_URL}/api/thumbnails`, {
    method: 'DELETE',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ paths }),
  });
  if (!response.ok)
    throw new Error(`${response.status} ${response.statusText}`);
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

export async function getUploadSession(
  session: Pick<UploadSession, 'statusUrl'>,
  signal?: AbortSignal
) {
  const response = await fetch(apiUrl(session.statusUrl), {
    headers: { Accept: 'application/json' },
    signal,
  });
  if (!response.ok) {
    throw new UploadHttpError(
      `Upload status failed: ${response.status} ${response.statusText}`,
      response.status
    );
  }
  return response.json() as Promise<UploadSession>;
}

export function completeUpload(session: UploadSession, signal?: AbortSignal) {
  return postJson<UploadSession>(session.completeUrl, {}, { signal });
}

export function cancelUpload(id: string) {
  return fetch(`${API_BASE_URL}/api/uploads/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}

export type GetDriftOptions = {
  query?: string;
  status?: string;
  limit?: number;
  offset?: number;
  refresh?: boolean;
};

type RawDriftObject = {
  name: string;
  size: number;
  generation: number;
  storageClass?: string;
};

type RawDriftEntry = {
  logicPath?: string;
  physicalHash: string;
  targetKey?: string;
  size: number;
  scope: string;
  status: string;
  actionable: boolean;
  object?: RawDriftObject;
  error?: string;
};

type RawDriftSnapshot = {
  snapshotStatus: string;
  scanning: boolean;
  entries: RawDriftEntry[];
  counts?: Record<string, number>;
  generatedAt?: string;
  objectRoot?: string;
  offset: number;
  limit: number;
  total: number;
};

type RawDriftPlan = {
  id: string;
  entries: Array<{
    logicPath: string;
    source: RawDriftObject;
    targetKey: string;
    shared: boolean;
  }>;
  cost: {
    usdMin: number;
    usdMax: number;
    pricingAsOf: string;
    breakdown?: Array<{
      name: string;
      storageClass?: string;
      units: number;
      unitLabel: string;
      rate: number;
      rateUnit: string;
      formula: string;
      usdMin: number;
      usdMax: number;
      details: string;
    }>;
    warnings?: string[];
  };
};

type RawDriftAction = {
  id: string;
  planId: string;
  idempotencyKey?: string;
  status: string;
  checkpoint?: string;
  entryIndex?: number;
  error?: string;
  createdAt?: string;
  updatedAt?: string;
};

function normalizeDriftSnapshot(
  raw: DriftResponse | RawDriftSnapshot,
  options: GetDriftOptions
): DriftResponse {
  if ('items' in raw) return raw;
  const counts = raw.counts ?? {};
  const items = raw.entries.map((entry) => ({
    logicPath: entry.logicPath || entry.physicalHash,
    currentKey: entry.physicalHash,
    targetKey: entry.targetKey || '',
    status: entry.status,
    size: entry.size,
    storageClass: entry.object?.storageClass || '',
    generation: entry.object?.generation ? String(entry.object.generation) : '',
    estimatedCostUsdMin: Number.NaN,
    estimatedCostUsdMax: Number.NaN,
    actionable: entry.actionable,
    scope: entry.scope,
    error: entry.error,
  }));
  const totalScanned = Object.values(counts).reduce(
    (total, count) => total + count,
    0
  );
  return {
    available: true,
    enabled: true,
    storageDriver: raw.objectRoot?.startsWith('gs://') ? 'gcs' : undefined,
    summary: {
      total: totalScanned,
      aligned: counts.aligned ?? 0,
      drifted: (counts.drifted ?? 0) + (counts.shared_object ?? 0),
      missing: counts.object_missing ?? 0,
      failed: (counts.size_mismatch ?? 0) + (counts.target_conflict ?? 0),
      totalBytes: Number.NaN,
      estimatedCostUsdMin: Number.NaN,
      estimatedCostUsdMax: Number.NaN,
      costBreakdown: [],
      costFormula: { minimum: '', maximum: '' },
      warnings: [],
    },
    items,
    pagination: {
      limit: raw.limit,
      offset: raw.offset,
      total: raw.total,
      query: options.query ?? '',
      hasNext: raw.offset + raw.limit < raw.total,
      hasPrev: raw.offset > 0,
    },
    pricingAsOf: '',
    pricingModel: '',
    pricingSources: [],
    generatedAt: raw.generatedAt ?? '',
  };
}

function normalizeDriftPlan(raw: DriftPlan | RawDriftPlan): DriftPlan {
  if ('planId' in raw) {
    return {
      ...raw,
      paths: raw.paths ?? raw.items.map((item) => item.logicPath),
    };
  }
  const items = raw.entries.map((entry) => ({
    logicPath: entry.logicPath,
    currentKey: entry.source.name,
    targetKey: entry.targetKey,
    status: entry.shared ? 'shared_object' : 'drifted',
    size: entry.source.size,
    storageClass: entry.source.storageClass ?? '',
    generation: String(entry.source.generation),
    estimatedCostUsdMin: Number.NaN,
    estimatedCostUsdMax: Number.NaN,
    actionable: true,
    method: 'copy_verify_delete',
  }));
  return {
    planId: raw.id,
    paths: items.map((item) => item.logicPath),
    items,
    totalBytes: items.reduce((total, item) => total + item.size, 0),
    estimatedCostUsdMin: raw.cost.usdMin,
    estimatedCostUsdMax: raw.cost.usdMax,
    pricingAsOf: raw.cost.pricingAsOf,
    method: 'copy_verify_delete',
    costBreakdown: raw.cost.breakdown,
    warnings: raw.cost.warnings,
  };
}

function normalizeDriftAction(
  raw: DriftAction | RawDriftAction,
  paths: string[]
): DriftAction {
  if ('progress' in raw) return raw;
  const progress = raw.entryIndex ?? 0;
  const failed = raw.status === 'failed' ? 1 : 0;
  const failedPath = failed ? paths[progress] : undefined;
  return {
    id: raw.id,
    planId: raw.planId,
    idempotencyKey: raw.idempotencyKey,
    status: raw.status,
    progress: raw.status === 'succeeded' ? paths.length : progress,
    total: paths.length,
    succeeded: Math.min(progress, paths.length),
    failed,
    failedPaths: failedPath ? [failedPath] : [],
    results: failedPath
      ? [{ logicPath: failedPath, status: 'failed', error: raw.error }]
      : undefined,
    error: raw.error,
    createdAt: raw.createdAt,
    updatedAt: raw.updatedAt,
  };
}

export async function getDrift(options: GetDriftOptions = {}) {
  const query = new URLSearchParams();
  const normalizedQuery = options.query?.trim();
  if (normalizedQuery) query.set('q', normalizedQuery);
  if (options.status && options.status !== 'all') {
    query.set('status', options.status);
  }
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  if (options.offset !== undefined) {
    query.set('offset', String(options.offset));
  }
  if (options.refresh) query.set('refresh', 'true');
  const suffix = query.size > 0 ? `?${query.toString()}` : '';
  const response = await requestJson<DriftResponse | RawDriftSnapshot>(
    `/api/drift${suffix}`
  );
  return normalizeDriftSnapshot(response, options);
}

export async function createDriftPlan(paths: string[]) {
  const response = await postJson<DriftPlan | RawDriftPlan>(
    '/api/drift/plans',
    { paths }
  );
  return normalizeDriftPlan(response);
}

export async function createDriftAction(
  planId: string,
  paths: string[],
  existingIdempotencyKey?: string
) {
  const idempotencyKey =
    existingIdempotencyKey ??
    `${planId}:${
      globalThis.crypto?.randomUUID?.() ??
      `drift-${Date.now()}-${Math.random().toString(36).slice(2)}`
    }`;
  const response = await postJson<DriftAction | RawDriftAction>(
    '/api/drift/actions',
    { planId, idempotencyKey }
  );
  const action = normalizeDriftAction(response, paths);
  return {
    ...action,
    idempotencyKey: action.idempotencyKey || idempotencyKey,
    results:
      action.results ??
      paths.map((logicPath) => ({ logicPath, status: 'pending' })),
  };
}

export async function getDriftAction(id: string, paths: string[]) {
  const response = await requestJson<DriftAction | RawDriftAction>(
    `/api/drift/actions/${encodeURIComponent(id)}`
  );
  return normalizeDriftAction(response, paths);
}

export async function getDriftActions() {
  const response = await requestJson<
    DriftActionsResponse | { actions: RawDriftAction[] }
  >('/api/drift/actions?all=true');
  return response.actions.map((action) => {
    const paths =
      'results' in action
        ? (action.results ?? []).map((result) => result.logicPath)
        : [];
    return normalizeDriftAction(action, paths);
  });
}

export function dismissDriftAction(id: string) {
  return deleteResource(`/api/drift/actions/${encodeURIComponent(id)}`);
}

export async function getCurrentDriftScan() {
  const response = await requestJson<DriftCurrentScanResponse>(
    '/api/drift/scans/current'
  );
  return response.scan ?? undefined;
}

export function startDriftScan() {
  return postJson<DriftScan>('/api/drift/scans', {});
}

export type UploadChunkResult = {
  uploadedSize: number;
  status: number;
};

export function committedOffsetFromRange(value: string | null) {
  if (!value) return undefined;
  const match = /bytes\s*=\s*0-(\d+)/i.exec(value);
  return match ? Number(match[1]) + 1 : undefined;
}

// Passing an 8 MiB Blob slice directly to XMLHttpRequest keeps memory bounded
// while preserving native upload progress events and GCS 308 Range headers.
export function putUploadChunk(
  session: UploadSession,
  chunk: Blob,
  start: number,
  total: number,
  onProgress: (uploaded: number, total: number) => void,
  signal?: AbortSignal
) {
  return new Promise<UploadChunkResult>((resolve, reject) => {
    const request = new XMLHttpRequest();
    let settled = false;

    const abortRequest = () => request.abort();
    const cleanup = () => signal?.removeEventListener('abort', abortRequest);
    const succeed = (result: UploadChunkResult) => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve(result);
    };
    const fail = (error: Error) => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(error);
    };
    if (signal?.aborted) {
      fail(new UploadHttpError('Upload paused'));
      return;
    }

    request.open(session.method, apiUrl(session.uploadUrl));
    for (const [name, value] of Object.entries(session.headers)) {
      request.setRequestHeader(name, value);
    }
    if (!session.headers['Content-Type'] && chunk.type) {
      request.setRequestHeader('Content-Type', chunk.type);
    }
    const end = start + chunk.size - 1;
    request.setRequestHeader(
      'Content-Range',
      total === 0 ? 'bytes */0' : `bytes ${start}-${end}/${total}`
    );
    request.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable) {
        onProgress(start + event.loaded, total);
      } else {
        onProgress(start + event.loaded, total);
      }
    });
    request.addEventListener('load', () => {
      if (
        (request.status >= 200 && request.status < 300) ||
        request.status === 308
      ) {
        const uploadedSize =
          committedOffsetFromRange(request.getResponseHeader('Range')) ??
          (total === 0 ? 0 : start + chunk.size);
        succeed({ uploadedSize, status: request.status });
        return;
      }
      fail(
        new UploadHttpError(
          `Upload failed: ${request.status} ${request.statusText}`,
          request.status
        )
      );
    });
    request.addEventListener('error', () =>
      fail(new UploadHttpError('Upload connection failed'))
    );
    request.addEventListener('abort', () =>
      fail(new UploadHttpError('Upload paused'))
    );
    signal?.addEventListener('abort', abortRequest, { once: true });
    try {
      request.send(chunk);
    } catch (error) {
      fail(
        error instanceof Error ? error : new UploadHttpError('Upload failed')
      );
    }
  });
}
