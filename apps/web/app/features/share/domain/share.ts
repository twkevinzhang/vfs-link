export type ShareStatus =
  | 'draft'
  | 'uploading'
  | 'completed'
  | 'notified'
  | 'notification_failed'
  | 'email_sent'
  | 'failed'
  | 'email_failed';

export type ShareRecord = {
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

const TERMINAL_STATUSES = new Set<ShareStatus>([
  'completed',
  'notified',
  'notification_failed',
  'email_sent',
  'failed',
  'email_failed',
]);

export function isTerminalShareStatus(status: ShareStatus) {
  return TERMINAL_STATUSES.has(status);
}

export function canStartShare(status: ShareStatus) {
  return ['draft', 'failed', 'notification_failed', 'email_failed'].includes(
    status
  );
}
