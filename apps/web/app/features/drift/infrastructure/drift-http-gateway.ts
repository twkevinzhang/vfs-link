import type {
  DriftGateway,
  GetDriftOptions,
} from '../application/drift-gateway';
import type {
  DriftAction,
  DriftActionsResponse,
  DriftCurrentScanResponse,
  DriftPlan,
  DriftResponse,
  DriftScan,
} from '../domain/drift';
import {
  deleteResource,
  postJson,
  requestJson,
} from '../../../shared/infrastructure/http/http-client';

export async function getDrift(options: GetDriftOptions = {}) {
  const query = new URLSearchParams();
  const normalizedQuery = options.query?.trim();
  if (normalizedQuery) query.set('q', normalizedQuery);
  if (options.status && options.status !== 'all')
    query.set('status', options.status);
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  if (options.offset !== undefined) query.set('offset', String(options.offset));
  if (options.refresh) query.set('refresh', 'true');
  const suffix = query.size > 0 ? `?${query.toString()}` : '';
  return requestJson<DriftResponse>(`/api/drift${suffix}`);
}

export async function createDriftPlan(paths: string[]) {
  return postJson<DriftPlan>('/api/drift/plans', { paths });
}

export async function createDriftAction(
  planId: string,
  existingIdempotencyKey?: string
) {
  const idempotencyKey =
    existingIdempotencyKey ??
    `${planId}:${
      globalThis.crypto?.randomUUID?.() ??
      `drift-${Date.now()}-${Math.random().toString(36).slice(2)}`
    }`;
  return postJson<DriftAction>('/api/drift/actions', {
    planId,
    idempotencyKey,
  });
}

export async function getDriftAction(id: string) {
  return requestJson<DriftAction>(
    `/api/drift/actions/${encodeURIComponent(id)}`
  );
}

export async function getDriftActions() {
  const response = await requestJson<DriftActionsResponse>(
    '/api/drift/actions?all=true'
  );
  return response.actions;
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

export const driftHttpGateway = {
  createDriftAction,
  createDriftPlan,
  dismissDriftAction,
  getCurrentDriftScan,
  getDrift,
  getDriftAction,
  getDriftActions,
  startDriftScan,
} satisfies DriftGateway;
