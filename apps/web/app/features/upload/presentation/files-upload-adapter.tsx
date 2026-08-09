import { lazy, Suspense } from 'react';

import { UploadActivity } from './upload-activity';
import { useBackgroundUploadQueue } from './upload-queue';

const loadDialog = () => import('./upload-dialog');
const LazyUploadDialog = lazy(() =>
  loadDialog().then((module) => ({ default: module.UploadDialog }))
);

export function FilesUploadActivityAdapter(props: {
  expanded: boolean;
  onExpandedChange(expanded: boolean): void;
  onRequestCancelAll(): void;
}) {
  const queue = useBackgroundUploadQueue();
  return <UploadActivity queue={queue} {...props} />;
}

export function FilesUploadDialogAdapter(props: {
  currentPath: string;
  open: boolean;
  onOpenChange(open: boolean): void;
}) {
  const queue = useBackgroundUploadQueue();
  return (
    <Suspense fallback={null}>
      <LazyUploadDialog
        {...props}
        onAddFiles={(candidates) => queue.add(candidates, props.currentPath)}
        onAddArchives={(batches) =>
          queue.addArchives(batches, props.currentPath)
        }
      />
    </Suspense>
  );
}
