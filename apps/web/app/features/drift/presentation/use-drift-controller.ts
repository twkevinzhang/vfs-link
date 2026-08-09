import { useEffect, useMemo, useSyncExternalStore } from 'react';

import {
  DriftController,
  type DriftScheduler,
} from '../application/drift-controller';
import type { DriftGateway } from '../application/drift-gateway';

export function useDriftController(
  gateway: DriftGateway,
  scheduler: DriftScheduler
) {
  const controller = useMemo(
    () => new DriftController(gateway, scheduler),
    [gateway, scheduler]
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
