export type FileViewMode = 'list' | 'grid';

const FILE_VIEW_MODE_STORAGE_KEY = 'vfs-link:file-view-mode';

export function parseFileViewMode(
  value: string | null | undefined
): FileViewMode {
  return value === 'grid' ? 'grid' : 'list';
}

export function readFileViewMode(
  storage: Pick<Storage, 'getItem'> | undefined
): FileViewMode {
  if (!storage) return 'list';

  try {
    return parseFileViewMode(storage.getItem(FILE_VIEW_MODE_STORAGE_KEY));
  } catch {
    return 'list';
  }
}

export function writeFileViewMode(
  storage: Pick<Storage, 'setItem'> | undefined,
  mode: FileViewMode
) {
  if (!storage) return;

  try {
    storage.setItem(FILE_VIEW_MODE_STORAGE_KEY, mode);
  } catch {
    // Browsers may reject localStorage in private or restricted contexts.
  }
}
