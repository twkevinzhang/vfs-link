import type { ShareGateway } from '../application/share-gateway';
import type { ShareRecord } from '../domain/share';
import {
  postJson,
  requestJson,
  type RequestOptions,
} from '../../../shared/infrastructure/http/http-client';

export function createShareDraft(path: string) {
  return postJson<ShareRecord>('/api/shares/drafts', { path });
}

export function getShare(id: string, options: RequestOptions = {}) {
  return requestJson<ShareRecord>(
    `/api/shares/${encodeURIComponent(id)}`,
    options
  );
}

export function startShare(id: string, options: RequestOptions = {}) {
  return postJson<ShareRecord>(
    `/api/shares/${encodeURIComponent(id)}/start`,
    {},
    options
  );
}

export const shareHttpGateway = {
  createShareDraft,
  getShare,
  startShare,
} satisfies ShareGateway;
