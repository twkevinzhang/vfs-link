import type { DriftAction, DriftItem } from './drift';

const TERMINAL_ACTION_STATUSES = new Set([
  'completed',
  'succeeded',
  'failed',
  'partial',
  'partially_failed',
  'partial_failure',
  'completed_with_errors',
  'cancelled',
]);

export function isDriftActionTerminal(status: string) {
  return TERMINAL_ACTION_STATUSES.has(status.toLowerCase());
}

export function driftActionFailedPaths(action: DriftAction) {
  const resultPaths = (action.results ?? [])
    .filter((result) =>
      ['failed', 'error'].includes(result.status.toLowerCase())
    )
    .map((result) => result.logicPath);
  return [...new Set([...(action.failedPaths ?? []), ...resultPaths])];
}

export function driftActionPaths(action: DriftAction) {
  return [
    ...new Set(
      (action.results ?? [])
        .map((result) => result.logicPath)
        .filter((path) => path.length > 0)
    ),
  ];
}

export function upsertDriftAction(
  actions: DriftAction[],
  action: DriftAction,
  prepend = false
) {
  const id = action.id || action.actionId;
  const existingIndex = actions.findIndex(
    (candidate) => (candidate.id || candidate.actionId) === id
  );
  if (existingIndex < 0)
    return prepend ? [action, ...actions] : [...actions, action];
  const next = [...actions];
  const existing = next[existingIndex];
  next[existingIndex] = {
    ...action,
    idempotencyKey: action.idempotencyKey || existing.idempotencyKey,
    total: action.total || existing.total,
    failedPaths:
      action.failedPaths && action.failedPaths.length > 0
        ? action.failedPaths
        : existing.failedPaths,
    results:
      action.results && action.results.length > 0
        ? action.results
        : existing.results,
  };
  return next;
}

export function markDriftActionRetrying(action: DriftAction) {
  if (!isDriftActionTerminal(action.status)) return action;
  return { ...action, status: 'pending', error: undefined };
}

export function isActionableDriftItem(item: DriftItem) {
  if (item.actionable !== undefined) return item.actionable;
  return [
    'drifted',
    'misaligned',
    'ready',
    'key_mismatch',
    'object_key_mismatch',
    'shared_object',
  ].includes(item.status.toLowerCase());
}
