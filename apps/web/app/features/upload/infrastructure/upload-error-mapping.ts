import {
  isTransientUploadFailure,
  shouldAutomaticallyRetryFailure,
  type UploadFailure,
} from '../domain/upload-queue';
import { HttpError } from '../../../shared/infrastructure/http/http-client';

export function classifyUploadError(error: unknown): UploadFailure {
  if (error instanceof TypeError) return { kind: 'network' };
  if (!(error instanceof HttpError)) return { kind: 'other' };
  if (error.status === undefined || error.status === 0)
    return { kind: 'network' };
  if (error.status === 408 || error.status === 425) return { kind: 'timeout' };
  if (error.status === 429) return { kind: 'rate-limit' };
  if (error.status >= 500) return { kind: 'server' };
  if (error.status === 409) return { kind: 'conflict', code: error.code };
  return { kind: 'other', code: error.code };
}

export function isTransientUploadError(error: unknown) {
  return isTransientUploadFailure(classifyUploadError(error));
}

export function shouldAutomaticallyRetry(
  error: unknown,
  retriesAlreadyUsed: number
) {
  return shouldAutomaticallyRetryFailure(
    classifyUploadError(error),
    retriesAlreadyUsed
  );
}

export function isOffsetConflict(error: unknown) {
  return error instanceof HttpError && error.status === 409;
}

export function isUploadTargetChanged(error: unknown) {
  return (
    error instanceof HttpError &&
    (error.code === 'UPLOAD_TARGET_CHANGED' ||
      error.code === 'UPLOAD_TARGET_EXISTS' ||
      error.code === 'UPLOAD_TARGET_IS_DIRECTORY')
  );
}
