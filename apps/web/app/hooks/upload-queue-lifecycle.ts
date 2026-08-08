import type { UploadQueueItem } from '../lib/upload-queue-model';
import type { UploadQueueLeadershipState } from '../lib/upload-queue-coordinator';
import { restoreUploadQueueItem } from '../lib/upload-queue-runtime';
import { loadUploadQueue } from '../lib/upload-queue-storage';

export async function hydrateUploadQueueLifecycle({
  isMounted,
  apply,
  markHydrated,
  persist,
  preflight,
}: {
  isMounted: () => boolean;
  apply: (items: UploadQueueItem[], globallyPaused: boolean) => void;
  markHydrated: () => void;
  persist: (items: UploadQueueItem[], globallyPaused: boolean) => void;
  preflight: (keys: string[]) => void;
}) {
  try {
    const { items: storedItems, globallyPaused } = await loadUploadQueue();
    const restored = await Promise.all(
      storedItems.map((stored) =>
        restoreUploadQueueItem(stored, globallyPaused)
      )
    );
    if (!isMounted()) return;
    apply(restored, globallyPaused);
    markHydrated();
    persist(restored, globallyPaused);
    const checkingKeys = restored
      .filter((item) => item.state === 'checking')
      .map((item) => item.key);
    if (checkingKeys.length > 0) preflight(checkingKeys);
  } catch {
    markHydrated();
  }
}

export function createUploadQueueLeadershipHandler({
  now = Date.now,
  isMounted,
  isHydrated,
  items,
  setLeader,
  setState,
  reload,
  schedule,
  preflight,
}: {
  now?: () => number;
  isMounted: () => boolean;
  isHydrated: () => boolean;
  items: () => UploadQueueItem[];
  setLeader: (isLeader: boolean) => void;
  setState: (state: UploadQueueLeadershipState) => void;
  reload: () => void;
  schedule: () => void;
  preflight: (keys: string[]) => void;
}) {
  let waitingSince = 0;
  return (state: UploadQueueLeadershipState) => {
    if (state === 'waiting') waitingSince = now();
    const becameLeader = state === 'leader';
    setLeader(becameLeader);
    if (isMounted()) setState(state);
    if (!becameLeader) return;
    if (waitingSince > 0 && now() - waitingSince > 1_000 && isHydrated()) {
      reload();
      return;
    }
    schedule();
    const checkingKeys = items()
      .filter((item) => item.state === 'checking')
      .map((item) => item.key);
    if (checkingKeys.length > 0) preflight(checkingKeys);
  };
}
