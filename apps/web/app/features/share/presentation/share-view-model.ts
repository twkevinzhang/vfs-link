import { canStartShare, type ShareStatus } from '../domain/share';

export const shareStatusLabels = {
  draft: 'draft',
  uploading: 'uploading',
  completed: 'completed',
  notified: 'notified',
  notification_failed: 'notification failed',
  email_sent: 'email sent',
  failed: 'failed',
  email_failed: 'email failed',
} satisfies Record<ShareStatus, string>;

export function shareViewState(status: ShareStatus, starting: boolean) {
  return {
    canStart: canStartShare(status),
    isBusy: status === 'uploading' || starting,
    isSuccessful: ['completed', 'notified', 'email_sent'].includes(status),
  };
}
