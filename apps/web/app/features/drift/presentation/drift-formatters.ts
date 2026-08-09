import type { DriftAction } from '../domain/drift';

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
  if (!Number.isFinite(min) || !Number.isFinite(max)) return 'Plan required';
  if (min === max) return formatUsd(min);
  return `${formatUsd(min)}–${formatUsd(max)}`;
}

export function driftStatusLabel(status: string) {
  const labels: Record<string, string> = {
    aligned: 'Aligned',
    succeeded: 'Completed',
    completed: 'Completed',
    drifted: 'Drifted',
    misaligned: 'Drifted',
    ready: 'Drifted',
    key_mismatch: 'Drifted',
    object_key_mismatch: 'Drifted',
    moving: 'Moving',
    running: 'Moving',
    missing: 'Missing object',
    object_missing: 'Missing object',
    size_mismatch: 'Size mismatch',
    target_conflict: 'Target conflict',
    shared_object: 'Shared object',
    orphan_object: 'Orphan object',
    failed: 'Failed',
    error: 'Failed',
  };
  return labels[status.toLowerCase()] ?? (status || 'Unknown');
}

export function driftMethodLabel(method: string | undefined) {
  const labels: Record<string, string> = {
    atomic_move: 'Atomic move',
    rename: 'Atomic move',
    copy_verify_delete: 'Copy · verify · delete',
    'copy-verify-delete': 'Copy · verify · delete',
    rewrite: 'Copy · verify · delete',
    copy_verify_update_delete: 'Copy · verify · update · delete',
    copy_verify_update_conditional_delete:
      'Copy · verify · update · conditional delete',
    copy_on_branch: 'Copy on shared branch',
    display_only: 'Display only',
    metadata_only: 'Metadata only',
  };
  if (!method) return 'Determined by plan';
  return labels[method.toLowerCase()] ?? method.replaceAll('_', ' ');
}
