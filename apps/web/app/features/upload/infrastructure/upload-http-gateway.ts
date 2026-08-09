import type {
  UploadPreflightItemInput,
  UploadPreflightResponse,
  UploadSession,
  UploadCancellation,
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
import type { BrowserUploadSourceRegistry } from './browser-upload-source-registry';

export { HttpError as UploadHttpError };

type UploadSessionDto = UploadSession & {
  logicPath: string;
  size: number;
  contentType: string;
  method: 'PUT';
  uploadUrl: string;
  headers: Record<string, string>;
  completeUrl: string;
  statusUrl: string;
};

function toUploadSession(dto: UploadSessionDto): UploadSession {
  return {
    id: dto.id,
    status: dto.status,
    uploadedSize: dto.uploadedSize,
    error: dto.error,
    expiresAt: dto.expiresAt,
  };
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

function createUploadSessionTransport() {
  const sessions = new Map<string, UploadSessionDto>();

  const getTransport = (id: string) => {
    const session = sessions.get(id);
    if (!session) throw new Error(`Upload transport not found: ${id}`);
    return session;
  };

  const remember = (dto: UploadSessionDto) => {
    sessions.set(dto.id, dto);
    return toUploadSession(dto);
  };

  return {
    getTransport,
    remember,
    delete: (id: string) => sessions.delete(id),
  };
}

async function fetchUploadSession(
  sessionId: string,
  transport: ReturnType<typeof createUploadSessionTransport>,
  cancellation?: UploadCancellation
) {
  const session = transport.getTransport(sessionId);
  const controller = new AbortController();
  if (cancellation?.aborted) controller.abort();
  const removeAbort = cancellation?.onAbort(() => controller.abort());
  let response: Response;
  try {
    response = await fetch(apiUrl(session.statusUrl), {
      headers: { Accept: 'application/json' },
      signal: controller.signal,
    });
  } finally {
    removeAbort?.();
  }
  if (!response.ok) {
    throw new HttpError(
      `Upload status failed: ${response.status} ${response.statusText}`,
      response.status
    );
  }
  return transport.remember((await response.json()) as UploadSessionDto);
}

async function completeUploadSession(
  sessionId: string,
  transport: ReturnType<typeof createUploadSessionTransport>,
  cancellation?: UploadCancellation
) {
  const session = transport.getTransport(sessionId);
  const controller = new AbortController();
  if (cancellation?.aborted) controller.abort();
  const removeAbort = cancellation?.onAbort(() => controller.abort());
  const dto = await postJson<UploadSessionDto>(
    session.completeUrl,
    {},
    { signal: controller.signal }
  ).finally(() => removeAbort?.());
  return transport.remember(dto);
}

export function committedOffsetFromRange(value: string | null) {
  if (!value) return undefined;
  const match = /bytes\s*=\s*0-(\d+)/i.exec(value);
  return match ? Number(match[1]) + 1 : undefined;
}

function putUploadChunk(
  sourceRegistry: BrowserUploadSourceRegistry,
  transport: ReturnType<typeof createUploadSessionTransport>,
  session: UploadSession,
  sourceId: string,
  start: number,
  endExclusive: number,
  total: number,
  onProgress: (uploaded: number, total: number) => void,
  cancellation?: UploadCancellation
) {
  const sessionDto = transport.getTransport(session.id);
  const source = sourceRegistry.get(sourceId);
  const chunk = source.slice(start, endExclusive, source.type);
  return new Promise<UploadChunkResult>((resolve, reject) => {
    const request = new XMLHttpRequest();
    let settled = false;
    const abortRequest = () => request.abort();
    const removeAbort = cancellation?.onAbort(abortRequest);
    const cleanup = () => removeAbort?.();
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
    if (cancellation?.aborted) {
      fail(new HttpError('Upload paused'));
      return;
    }
    request.open(sessionDto.method, apiUrl(sessionDto.uploadUrl));
    for (const [name, value] of Object.entries(sessionDto.headers)) {
      request.setRequestHeader(name, value);
    }
    if (!sessionDto.headers['Content-Type'] && chunk.type) {
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
    try {
      request.send(chunk);
    } catch (error) {
      fail(error instanceof Error ? error : new HttpError('Upload failed'));
    }
  });
}

export function createUploadHttpGateway(
  sourceRegistry: BrowserUploadSourceRegistry
) {
  const transport = createUploadSessionTransport();
  return {
    cancelUpload: async (id) => {
      try {
        await deleteResource(`/api/uploads/${encodeURIComponent(id)}`);
      } finally {
        transport.delete(id);
      }
    },
    completeUpload: (
      session: UploadSession,
      cancellation?: UploadCancellation
    ) => completeUploadSession(session.id, transport, cancellation),
    createUpload: async (input) =>
      transport.remember(
        await postJson<UploadSessionDto>('/api/uploads', input)
      ),
    getUploadSession: (
      session: Pick<UploadSession, 'id'>,
      cancellation?: UploadCancellation
    ) => fetchUploadSession(session.id, transport, cancellation),
    preflightUploads,
    putUploadChunk: (
      session,
      sourceId,
      start,
      endExclusive,
      total,
      onProgress,
      currentCancellation
    ) =>
      putUploadChunk(
        sourceRegistry,
        transport,
        session,
        sourceId,
        start,
        endExclusive,
        total,
        onProgress,
        currentCancellation
      ),
  } satisfies UploadGateway;
}
