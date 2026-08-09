import {
  createContext,
  createElement,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useSyncExternalStore,
  type ReactNode,
} from 'react';

import { UploadQueueController } from '../application/upload-queue-controller';
import type { UploadQueueDependencies } from '../application/upload-queue-dependencies';
import type {
  PreparedArchiveBatch,
  UploadCandidate,
} from './upload-candidates';

export type { UploadQueueItem, UploadQueueState } from '../domain/upload-queue';

function normalizePath(value: string) {
  return value
    .replaceAll('\\', '/')
    .split('/')
    .filter((part) => part && part !== '.' && part !== '..')
    .join('/');
}

function useQueueAdapter(
  controller: UploadQueueController,
  dependencies: UploadQueueDependencies
) {
  const snapshot = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
    controller.getSnapshot
  );

  return useMemo(() => {
    const registerCandidates = (candidates: UploadCandidate[]) =>
      candidates.map((candidate) => ({
        ...dependencies.sources.register(candidate.file, {
          name: candidate.file.name,
          size: candidate.file.size,
          lastModified: candidate.file.lastModified,
          contentType: candidate.file.type || 'application/octet-stream',
        }),
        relativePath: candidate.relativePath,
        archiveGroupId: candidate.archiveGroupId,
      }));

    return {
      ...snapshot,
      add(candidates: UploadCandidate[], destinationPath: string) {
        controller.add(registerCandidates(candidates), destinationPath);
      },
      addArchives(batches: PreparedArchiveBatch[], destinationPath: string) {
        controller.addArchives(
          batches.map((batch) => {
            const thumbnail = batch.thumbnail
              ? {
                  ...dependencies.sources.register(batch.thumbnail.blob, {
                    name: 'thumbnail.webp',
                    size: batch.thumbnail.blob.size,
                    lastModified: Date.now(),
                    contentType: batch.thumbnail.blob.type || 'image/webp',
                  }),
                  width: batch.thumbnail.width,
                  height: batch.thumbnail.height,
                }
              : undefined;
            return {
              id: batch.id,
              sources: registerCandidates(batch.candidates),
              paths: batch.candidates.map((candidate) =>
                normalizePath(`${destinationPath}/${candidate.relativePath}`)
              ),
              thumbnail,
            };
          }),
          destinationPath
        );
      },
      retry: controller.retry,
      retryAll: controller.retryAll,
      replaceOne: controller.replaceOne,
      skipOne: controller.skipOne,
      replaceAll: controller.replaceAll,
      skipAll: controller.skipAll,
      dismiss: controller.dismiss,
      cancel: controller.cancel,
      cancelAll: controller.cancelAll,
      pause: controller.pause,
      resume: controller.resume,
      pauseAll: controller.pauseAll,
      resumeAll: controller.resumeAll,
    };
  }, [controller, dependencies.sources, snapshot]);
}

/** Standalone adapter retained for isolated component tests and composition. */
export function useUploadQueue({
  dependencies,
}: {
  dependencies: UploadQueueDependencies;
}) {
  const controllerRef = useRef<UploadQueueController | undefined>(undefined);
  if (!controllerRef.current) {
    controllerRef.current = new UploadQueueController(dependencies);
  }
  useEffect(() => () => controllerRef.current?.dispose(), []);
  return useQueueAdapter(controllerRef.current, dependencies);
}

export type UploadQueue = ReturnType<typeof useUploadQueue>;
type UploadContextValue = {
  controller: UploadQueueController;
  dependencies: UploadQueueDependencies;
};
const UploadQueueContext = createContext<UploadContextValue | null>(null);

export function UploadQueueProvider({
  children,
  dependencies,
}: {
  children: ReactNode;
  dependencies: UploadQueueDependencies;
}) {
  const controllerRef = useRef<UploadQueueController | undefined>(undefined);
  if (!controllerRef.current) {
    controllerRef.current = new UploadQueueController(dependencies);
  }
  const controller = controllerRef.current;
  const structure = useSyncExternalStore(
    controller.subscribeStructure,
    controller.getStructureSnapshot,
    controller.getStructureSnapshot
  );
  useEffect(() => () => controller.dispose(), [controller]);
  useEffect(() => {
    const hasPending =
      structure.summary.checking +
        structure.summary.queued +
        structure.summary.uploading +
        structure.summary.retrying >
      0;
    if (!hasPending) return;
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', warnBeforeUnload);
    return () => window.removeEventListener('beforeunload', warnBeforeUnload);
  }, [structure]);
  const value = useMemo(
    () => ({ controller, dependencies }),
    [controller, dependencies]
  );
  return createElement(UploadQueueContext.Provider, { value }, children);
}

function useUploadContext() {
  const context = useContext(UploadQueueContext);
  if (!context) {
    throw new Error(
      'Upload queue hook must be used inside UploadQueueProvider'
    );
  }
  return context;
}

export function useBackgroundUploadQueue() {
  const { controller, dependencies } = useUploadContext();
  return useQueueAdapter(controller, dependencies);
}

/** Files consumes only structural queue changes, never byte progress ticks. */
export function useFilesUploadQueue() {
  const { controller } = useUploadContext();
  const snapshot = useSyncExternalStore(
    controller.subscribeStructure,
    controller.getStructureSnapshot,
    controller.getStructureSnapshot
  );
  return useMemo(
    () => ({ ...snapshot, cancelAll: controller.cancelAll }),
    [controller.cancelAll, snapshot]
  );
}
