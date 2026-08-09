import { useEffect, useMemo, useSyncExternalStore } from 'react';

import {
  ShareController,
  type ShareScheduler,
} from '../application/share-controller';
import type { ShareGateway } from '../application/share-gateway';

export function useShareController(
  shareId: string | undefined,
  gateway: ShareGateway,
  scheduler: ShareScheduler
) {
  const controller = useMemo(
    () => new ShareController(shareId, gateway, scheduler),
    [gateway, scheduler, shareId]
  );
  const snapshot = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
    controller.getSnapshot
  );

  useEffect(() => {
    controller.start();
    return controller.dispose;
  }, [controller]);

  return snapshot;
}
