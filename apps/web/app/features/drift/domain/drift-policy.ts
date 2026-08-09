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
  return action.results
    .filter((result) =>
      ['failed', 'error'].includes(result.status.toLowerCase())
    )
    .map((result) => result.logicPath);
}

export function driftActionPaths(action: DriftAction) {
  return [
    ...new Set(
      action.results
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
  const id = action.id;
  const existingIndex = actions.findIndex((candidate) => candidate.id === id);
  if (existingIndex < 0)
    return prepend ? [action, ...actions] : [...actions, action];
  const next = [...actions];
  next[existingIndex] = action;
  return next;
}

export function markDriftActionRetrying(action: DriftAction) {
  if (!isDriftActionTerminal(action.status)) return action;
  return { ...action, status: 'pending', error: undefined };
}

export function isActionableDriftItem(item: DriftItem) {
  return item.actionable;
}
