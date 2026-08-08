import { describe, expect, it } from 'vitest';

import { UploadHttpError, committedOffsetFromRange } from './api';
import {
  MAX_AUTOMATIC_RETRIES,
  MAX_CONCURRENT_UPLOADS,
  UPLOAD_CHUNK_SIZE,
  isOffsetConflict,
  isRetryAllEligible,
  isTransientUploadError,
  matchesFingerprint,
  nextChunkRange,
  retryDelayMs,
  shouldAutomaticallyRetry,
} from './upload-queue-core';

describe('upload queue coordinator', () => {
  it('uses fixed 8 MiB chunks and preserves the committed offset', () => {
    expect(UPLOAD_CHUNK_SIZE).toBe(8 * 1024 * 1024);
    expect(
      nextChunkRange(UPLOAD_CHUNK_SIZE, UPLOAD_CHUNK_SIZE * 3 + 4)
    ).toEqual({
      start: UPLOAD_CHUNK_SIZE,
      endExclusive: UPLOAD_CHUNK_SIZE * 2,
    });
    expect(
      nextChunkRange(UPLOAD_CHUNK_SIZE * 3, UPLOAD_CHUNK_SIZE * 3 + 4)
    ).toEqual({
      start: UPLOAD_CHUNK_SIZE * 3,
      endExclusive: UPLOAD_CHUNK_SIZE * 3 + 4,
    });
  });

  it('converts the inclusive server Range into the next upload offset', () => {
    expect(committedOffsetFromRange('bytes=0-8388607')).toBe(8 * 1024 * 1024);
    expect(committedOffsetFromRange(null)).toBeUndefined();
    expect(committedOffsetFromRange('invalid')).toBeUndefined();
  });

  it('allows five automatic retries for transient failures only', () => {
    expect(MAX_AUTOMATIC_RETRIES).toBe(5);
    expect(MAX_CONCURRENT_UPLOADS).toBe(3);
    for (const status of [undefined, 408, 425, 429, 500, 503]) {
      expect(
        isTransientUploadError(new UploadHttpError('failed', status))
      ).toBe(true);
    }
    for (const status of [400, 401, 403, 409, 410, 412]) {
      expect(
        isTransientUploadError(new UploadHttpError('failed', status))
      ).toBe(false);
    }
    expect(isTransientUploadError(new TypeError('Failed to fetch'))).toBe(true);
    expect(isTransientUploadError(new Error('programming error'))).toBe(false);
    expect(isOffsetConflict(new UploadHttpError('offset', 409))).toBe(true);
    const transient = new UploadHttpError('offline');
    for (let retriesUsed = 0; retriesUsed < 5; retriesUsed += 1) {
      expect(shouldAutomaticallyRetry(transient, retriesUsed)).toBe(true);
    }
    expect(shouldAutomaticallyRetry(transient, 5)).toBe(false);
  });

  it('only includes retry-eligible failures with an available source', () => {
    expect(isRetryAllEligible('failed', true, true)).toBe(true);
    expect(isRetryAllEligible('failed', false, true)).toBe(false);
    expect(isRetryAllEligible('paused', true, true)).toBe(false);
    expect(isRetryAllEligible('failed', true, false)).toBe(false);
  });

  it('uses capped exponential backoff with injectable jitter', () => {
    expect(retryDelayMs(1, () => 0.5)).toBe(500);
    expect(retryDelayMs(5, () => 0.5)).toBe(8_000);
    expect(retryDelayMs(6, () => 0.5)).toBe(15_000);
  });

  it('requires a reselected source to match the original fingerprint', () => {
    const original = new File(['abc'], 'source.txt', { lastModified: 123 });
    expect(
      matchesFingerprint(original, {
        name: 'source.txt',
        size: 3,
        lastModified: 123,
      })
    ).toBe(true);
    expect(
      matchesFingerprint(original, {
        name: 'source.txt',
        size: 4,
        lastModified: 123,
      })
    ).toBe(false);
  });
});
