export const INITIAL_DIALOG_ROW_LIMIT = 100;
export const DIALOG_ROW_PAGE_SIZE = 100;

export function visibleDialogRows<T>(items: readonly T[], limit: number) {
  return items.slice(0, Math.max(0, Math.min(items.length, Math.floor(limit))));
}

export function nextDialogRowLimit(
  current: number,
  total: number,
  pageSize = DIALOG_ROW_PAGE_SIZE
) {
  return Math.min(total, Math.max(0, current) + Math.max(1, pageSize));
}

export function searchableThumbnailOptions<T extends { relativePath: string }>(
  candidates: readonly T[],
  query: string,
  selectedPath: string | undefined,
  limit = INITIAL_DIALOG_ROW_LIMIT
) {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const matches = normalizedQuery
    ? candidates.filter((candidate) =>
        candidate.relativePath.toLocaleLowerCase().includes(normalizedQuery)
      )
    : candidates;
  const boundedLimit = Math.max(1, Math.floor(limit));
  const visible = matches.slice(0, boundedLimit);
  if (
    !selectedPath ||
    visible.some((candidate) => candidate.relativePath === selectedPath)
  ) {
    return visible;
  }
  const selected = candidates.find(
    (candidate) => candidate.relativePath === selectedPath
  );
  return selected ? [...visible.slice(0, boundedLimit - 1), selected] : visible;
}

export async function mapWithConcurrency<T, R>(
  items: readonly T[],
  concurrency: number,
  mapper: (item: T, index: number, signal?: AbortSignal) => Promise<R>,
  signal?: AbortSignal
) {
  signal?.throwIfAborted();
  const results = new Array<R>(items.length);
  let nextIndex = 0;
  const workers = Array.from(
    { length: Math.min(items.length, Math.max(1, Math.floor(concurrency))) },
    async () => {
      for (;;) {
        signal?.throwIfAborted();
        const index = nextIndex;
        nextIndex += 1;
        if (index >= items.length) return;
        const result = await mapper(items[index], index, signal);
        signal?.throwIfAborted();
        results[index] = result;
      }
    }
  );
  const work = Promise.all(workers).then(() => results);
  if (!signal) return work;
  let removeAbort: () => void = () => undefined;
  const aborted = new Promise<never>((_resolve, reject) => {
    const onAbort = () => reject(signal.reason);
    signal.addEventListener('abort', onAbort, { once: true });
    removeAbort = () => signal.removeEventListener('abort', onAbort);
  });
  try {
    return await Promise.race([work, aborted]);
  } finally {
    removeAbort();
  }
}
