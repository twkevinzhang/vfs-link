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
