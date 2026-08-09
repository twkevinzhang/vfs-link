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
    breakdown?: DriftPlan['costBreakdown'];
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
  if (options.status && options.status !== 'all')
    query.set('status', options.status);
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  if (options.offset !== undefined) query.set('offset', String(options.offset));
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
  return normalizeDriftAction(
    await requestJson<DriftAction | RawDriftAction>(
      `/api/drift/actions/${encodeURIComponent(id)}`
    ),
    paths
  );
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
