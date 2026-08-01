import type { DriftAction, DriftItem } from '../types/drift';

const terminalActionStatuses = new Set([
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
  return terminalActionStatuses.has(status.toLowerCase());
}

export function driftActionFailedPaths(action: DriftAction) {
  const resultPaths = (action.results ?? [])
    .filter((result) =>
      ['failed', 'error'].includes(result.status.toLowerCase())
    )
    .map((result) => result.logicPath);
  return [...new Set([...(action.failedPaths ?? []), ...resultPaths])];
}

export function driftActionPercent(action: DriftAction) {
  if (action.total <= 0) return 0;
  return Math.min(100, Math.max(0, (action.progress / action.total) * 100));
}

export function formatUsd(value: number) {
  if (!Number.isFinite(value) || value < 0) return 'US$0.00';
  if (value > 0 && value < 0.01) return '< US$0.01';
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: 4,
  }).format(value);
}

export function formatUsdRange(min: number, max: number) {
  if (!Number.isFinite(min) || !Number.isFinite(max)) {
    return 'Plan required';
  }
  if (min === max) return formatUsd(min);
  return `${formatUsd(min)}–${formatUsd(max)}`;
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

export function driftStatusLabel(status: string) {
  switch (status.toLowerCase()) {
    case 'aligned':
      return 'Aligned';
    case 'succeeded':
    case 'completed':
      return 'Completed';
    case 'drifted':
    case 'misaligned':
    case 'ready':
    case 'key_mismatch':
    case 'object_key_mismatch':
      return 'Drifted';
    case 'moving':
    case 'running':
      return 'Moving';
    case 'missing':
    case 'object_missing':
      return 'Missing object';
    case 'size_mismatch':
      return 'Size mismatch';
    case 'target_conflict':
      return 'Target conflict';
    case 'shared_object':
      return 'Shared object';
    case 'orphan_object':
      return 'Orphan object';
    case 'failed':
    case 'error':
      return 'Failed';
    default:
      return status || 'Unknown';
  }
}

export function driftMethodLabel(method: string | undefined) {
  switch (method?.toLowerCase()) {
    case 'atomic_move':
    case 'rename':
      return 'Atomic move';
    case 'copy_verify_delete':
    case 'copy-verify-delete':
    case 'rewrite':
      return 'Copy · verify · delete';
    case 'copy_verify_update_delete':
      return 'Copy · verify · update · delete';
    case 'copy_verify_update_conditional_delete':
      return 'Copy · verify · update · conditional delete';
    case 'copy_on_branch':
      return 'Copy on shared branch';
    case 'display_only':
      return 'Display only';
    case 'metadata_only':
      return 'Metadata only';
    case undefined:
    case '':
      return 'Determined by plan';
    default:
      return (method ?? '').replaceAll('_', ' ');
  }
}
