import type {
  CreateUploadInput,
  UploadPreflightItemInput,
  UploadPreflightResponse,
  UploadSession,
} from '../application/upload-contracts';
import type {
  UploadChunkResult,
  UploadGateway,
} from '../application/upload-gateway';
import {
  HttpError,
  apiUrl,
  deleteResource,
  postJson,
} from '../../../shared/infrastructure/http/http-client';

export { HttpError as UploadHttpError };

export function createUpload(input: CreateUploadInput) {
  return postJson<UploadSession>('/api/uploads', input);
}

export async function preflightUploads(items: UploadPreflightItemInput[]) {
  const results: UploadPreflightResponse['items'] = [];
  for (let start = 0; start < items.length; start += 1000) {
    const response = await postJson<UploadPreflightResponse>(
      '/api/uploads/preflight',
      { items: items.slice(start, start + 1000) }
    );
    results.push(...response.items);
  }
  return { items: results };
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
    throw new HttpError(
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
  return deleteResource(`/api/uploads/${encodeURIComponent(id)}`);
}

export function committedOffsetFromRange(value: string | null) {
  if (!value) return undefined;
  const match = /bytes\s*=\s*0-(\d+)/i.exec(value);
  return match ? Number(match[1]) + 1 : undefined;
}

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
      fail(new HttpError('Upload paused'));
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
      onProgress(start + event.loaded, total);
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
        new HttpError(
          `Upload failed: ${request.status} ${request.statusText}`,
          request.status
        )
      );
    });
    request.addEventListener('error', () =>
      fail(new HttpError('Upload connection failed'))
    );
    request.addEventListener('abort', () =>
      fail(new HttpError('Upload paused'))
    );
    signal?.addEventListener('abort', abortRequest, { once: true });
    try {
      request.send(chunk);
    } catch (error) {
      fail(error instanceof Error ? error : new HttpError('Upload failed'));
    }
  });
}

export const uploadHttpGateway = {
  cancelUpload,
  completeUpload,
  createUpload,
  getUploadSession,
  preflightUploads,
  putUploadChunk,
} satisfies UploadGateway;
