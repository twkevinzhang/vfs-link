export const INITIAL_UPLOAD_ROW_LIMIT = 100;
export const UPLOAD_ROW_PAGE_SIZE = 100;

export function visibleUploadRows<T>(items: readonly T[], limit: number) {
  const boundedLimit = Math.max(0, Math.min(items.length, Math.floor(limit)));
  return items.slice(0, boundedLimit);
}

export function nextUploadRowLimit(
  currentLimit: number,
  total: number,
  pageSize = UPLOAD_ROW_PAGE_SIZE
) {
  return Math.min(total, Math.max(0, currentLimit) + Math.max(1, pageSize));
}
