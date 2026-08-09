import type {
  ShareGateway,
  ShareRequestCancellation,
} from '../application/share-gateway';
import type { ShareRecord, ShareStatus } from '../domain/share';
import {
  postJson,
  requestJson,
  type RequestOptions,
} from '../../../shared/infrastructure/http/http-client';

type ShareDto = {
  id: string;
  logicPath: string;
  fileName: string;
  size: number;
  destinationObject: string;
  destinationUrl: string;
  shareUrl: string;
  email: string;
  notificationTarget: string;
  status: ShareStatus;
  error?: string;
  createdAt: string;
  updatedAt: string;
  completedAt?: string;
  notifiedAt?: string;
};

function mapShareRecord(dto: ShareDto): ShareRecord {
  return {
    id: dto.id,
    logicPath: dto.logicPath,
    fileName: dto.fileName,
    size: dto.size,
    destinationObject: dto.destinationObject,
    destinationUrl: dto.destinationUrl,
    shareUrl: dto.shareUrl,
    email: dto.email,
    notificationTarget: dto.notificationTarget,
    status: dto.status,
    error: dto.error,
    createdAt: dto.createdAt,
    updatedAt: dto.updatedAt,
    completedAt: dto.completedAt,
    notifiedAt: dto.notifiedAt,
  };
}

async function withCancellation<T>(
  cancellation: ShareRequestCancellation | undefined,
  request: (signal: AbortSignal) => Promise<T>
) {
  const controller = new AbortController();
  const unsubscribe = cancellation?.onCancel(() => controller.abort());
  if (cancellation?.cancelled) controller.abort();
  try {
    return await request(controller.signal);
  } finally {
    unsubscribe?.();
  }
}

export async function createShareDraft(path: string) {
  return mapShareRecord(
    await postJson<ShareDto>('/api/shares/drafts', { path })
  );
}

export function getShare(id: string, cancellation?: ShareRequestCancellation) {
  return withCancellation(cancellation, async (signal) =>
    mapShareRecord(
      await requestJson<ShareDto>(`/api/shares/${encodeURIComponent(id)}`, {
        signal,
      } satisfies RequestOptions)
    )
  );
}

export function startShare(
  id: string,
  cancellation?: ShareRequestCancellation
) {
  return withCancellation(cancellation, async (signal) =>
    mapShareRecord(
      await postJson<ShareDto>(
        `/api/shares/${encodeURIComponent(id)}/start`,
        {},
        { signal } satisfies RequestOptions
      )
    )
  );
}

export const shareHttpGateway = {
  createShareDraft,
  getShare,
  startShare,
} satisfies ShareGateway;
