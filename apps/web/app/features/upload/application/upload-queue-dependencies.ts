import type { UploadGateway } from './upload-gateway';
import type { UploadSourceDescriptor } from './upload-contracts';
import type { UploadCancellation } from './upload-contracts';

export type UploadQueueDependencies = {
  gateway: UploadGateway;
  errors: {
    isOffsetConflict(error: unknown): boolean;
    isTransient(error: unknown): boolean;
    isTargetChanged(error: unknown): boolean;
    shouldAutomaticallyRetry(
      error: unknown,
      retriesAlreadyUsed: number
    ): boolean;
  };
  sources: {
    register(
      source: unknown,
      metadata: Omit<UploadSourceDescriptor, 'sourceId'>
    ): UploadSourceDescriptor;
    release(sourceId: string): void;
    clear(): void;
  };
  thumbnails: {
    save(input: {
      paths: string[];
      sourceId: string;
      width: number;
      height: number;
    }): Promise<void>;
    clear(paths: string[]): Promise<void>;
  };
  runtime: {
    now(): number;
    sleep(delayMs: number, cancellation: UploadCancellation): Promise<void>;
    scheduleFrame(callback: () => void): () => void;
  };
};
