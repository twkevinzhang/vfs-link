const UPLOAD_QUEUE_LEADER_LOCK = 'vfs-link-upload-queue-leader-v1';

export type UploadQueueLeadershipState = 'waiting' | 'leader' | 'stopped';

type LockManagerLike = {
  request: (
    name: string,
    options: { mode: 'exclusive'; signal: AbortSignal },
    callback: (lock: Lock | null) => Promise<void>
  ) => Promise<void>;
};

function waitForAbort(signal: AbortSignal) {
  if (signal.aborted) return Promise.resolve();
  return new Promise<void>((resolve) =>
    signal.addEventListener('abort', () => resolve(), { once: true })
  );
}

/** Holds a browser-wide exclusive upload scheduler lease until aborted. */
export async function holdUploadQueueLeadership({
  locks,
  signal,
  onState,
}: {
  locks?: LockManagerLike;
  signal: AbortSignal;
  onState: (state: UploadQueueLeadershipState) => void;
}) {
  if (!locks) {
    onState('leader');
    await waitForAbort(signal);
    onState('stopped');
    return;
  }

  onState('waiting');
  try {
    await locks.request(
      UPLOAD_QUEUE_LEADER_LOCK,
      { mode: 'exclusive', signal },
      async (lock) => {
        if (!lock || signal.aborted) return;
        onState('leader');
        await waitForAbort(signal);
      }
    );
  } catch (error) {
    if ((error as { name?: string }).name !== 'AbortError') throw error;
  } finally {
    onState('stopped');
  }
}
