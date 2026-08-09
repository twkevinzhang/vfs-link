const OPFS_DIRECTORY = '_vfs-link-archive-output';
const TEMPORARY_FILE_PREFIX = 'vfs-archive-v1-';
const TEMPORARY_FILE_SEPARATOR = '--';
const ARCHIVE_TEMPORARY_MANIFEST_VERSION = 1 as const;

export type { ArchiveTemporaryManifest } from '../features/upload/domain/archive-manifest';

export type ArchiveTemporaryFileUsage = {
  name: string;
  size: number;
  lastModified: number;
  ownerId?: string;
};

export type ArchiveTemporaryStorageUsage = {
  files: ArchiveTemporaryFileUsage[];
  fileCount: number;
  totalBytes: number;
};

export function supportsArchiveTemporaryStorage() {
  return (
    typeof navigator !== 'undefined' &&
    typeof navigator.storage?.getDirectory === 'function'
  );
}

function isSafeTemporaryIdentifier(value: string) {
  return (
    /^[A-Za-z0-9_-]+$/.test(value) && !value.includes(TEMPORARY_FILE_SEPARATOR)
  );
}

export function createArchiveTemporaryName(
  ownerId: string,
  fileId: string = crypto.randomUUID()
) {
  if (
    !isSafeTemporaryIdentifier(ownerId) ||
    !isSafeTemporaryIdentifier(fileId)
  ) {
    throw new TypeError('Archive temporary identifiers are invalid');
  }
  return `${TEMPORARY_FILE_PREFIX}${ownerId}${TEMPORARY_FILE_SEPARATOR}${fileId}`;
}

export function getArchiveTemporaryOwnerId(name: string) {
  if (!name.startsWith(TEMPORARY_FILE_PREFIX)) return undefined;
  const remainder = name.slice(TEMPORARY_FILE_PREFIX.length);
  const separatorIndex = remainder.indexOf(TEMPORARY_FILE_SEPARATOR);
  if (separatorIndex <= 0) return undefined;
  const ownerId = remainder.slice(0, separatorIndex);
  const fileId = remainder.slice(
    separatorIndex + TEMPORARY_FILE_SEPARATOR.length
  );
  if (
    !isSafeTemporaryIdentifier(ownerId) ||
    !isSafeTemporaryIdentifier(fileId)
  ) {
    return undefined;
  }
  return ownerId;
}

export function createArchiveTemporaryManifest(
  ownerId: string,
  files: Array<{ name: string; size: number }>,
  createdAt = Date.now()
): ArchiveTemporaryManifest {
  if (!isSafeTemporaryIdentifier(ownerId)) {
    throw new TypeError('Archive temporary owner is invalid');
  }
  if (!Number.isFinite(createdAt) || createdAt < 0) {
    throw new TypeError('Archive temporary creation time is invalid');
  }
  const normalizedFiles = files.map((file) => {
    if (
      getArchiveTemporaryOwnerId(file.name) !== ownerId ||
      !Number.isFinite(file.size) ||
      file.size < 0
    ) {
      throw new TypeError('Archive temporary file metadata is invalid');
    }
    return { name: file.name, size: file.size };
  });
  return {
    version: ARCHIVE_TEMPORARY_MANIFEST_VERSION,
    ownerId,
    createdAt,
    files: normalizedFiles,
  };
}

export function isArchiveTemporaryManifest(
  value: unknown
): value is ArchiveTemporaryManifest {
  if (!value || typeof value !== 'object') return false;
  const manifest = value as Partial<ArchiveTemporaryManifest>;
  return (
    manifest.version === ARCHIVE_TEMPORARY_MANIFEST_VERSION &&
    typeof manifest.ownerId === 'string' &&
    isSafeTemporaryIdentifier(manifest.ownerId) &&
    typeof manifest.createdAt === 'number' &&
    Number.isFinite(manifest.createdAt) &&
    manifest.createdAt >= 0 &&
    Array.isArray(manifest.files) &&
    manifest.files.every(
      (file) =>
        file !== null &&
        typeof file === 'object' &&
        typeof file.name === 'string' &&
        getArchiveTemporaryOwnerId(file.name) === manifest.ownerId &&
        typeof file.size === 'number' &&
        Number.isFinite(file.size) &&
        file.size >= 0
    )
  );
}

export function summarizeArchiveTemporaryUsage(
  files: ArchiveTemporaryFileUsage[]
): ArchiveTemporaryStorageUsage {
  return {
    files: [...files].sort((left, right) =>
      left.name.localeCompare(right.name)
    ),
    fileCount: files.length,
    totalBytes: files.reduce((total, file) => total + file.size, 0),
  };
}

export function findArchiveTemporaryOrphanNames(
  files: ArchiveTemporaryFileUsage[],
  manifests: unknown[],
  olderThan: number
) {
  if (!Number.isFinite(olderThan) || olderThan < 0) {
    throw new TypeError('Archive temporary cutoff is invalid');
  }
  const retainedNames = new Set(
    manifests
      .filter(isArchiveTemporaryManifest)
      .flatMap((manifest) => manifest.files.map((file) => file.name))
  );
  return files
    .filter(
      (file) => file.lastModified <= olderThan && !retainedNames.has(file.name)
    )
    .map((file) => file.name);
}

function isNotFoundError(error: unknown) {
  return (
    error !== null &&
    typeof error === 'object' &&
    'name' in error &&
    error.name === 'NotFoundError'
  );
}

export async function getArchiveOutputDirectory(create: boolean) {
  if (!supportsArchiveTemporaryStorage()) return undefined;
  const root = await navigator.storage.getDirectory();
  try {
    return await root.getDirectoryHandle(OPFS_DIRECTORY, { create });
  } catch (error) {
    if (!create && isNotFoundError(error)) return undefined;
    throw error;
  }
}

export async function removeArchiveTemporaryFiles(names: string[]) {
  if (!supportsArchiveTemporaryStorage() || names.length === 0) return;
  const directory = await getArchiveOutputDirectory(false);
  if (!directory) return;
  await Promise.all(
    [...new Set(names)].map((name) =>
      directory.removeEntry(name).catch(() => undefined)
    )
  );
}

export async function listArchiveTemporaryStorageUsage() {
  const directory = await getArchiveOutputDirectory(false);
  if (!directory) return summarizeArchiveTemporaryUsage([]);
  const iterableDirectory = directory as FileSystemDirectoryHandle & {
    entries: () => AsyncIterableIterator<[string, FileSystemHandle]>;
  };
  const files: ArchiveTemporaryFileUsage[] = [];
  for await (const [name, handle] of iterableDirectory.entries()) {
    if (handle.kind !== 'file') continue;
    const file = await (handle as FileSystemFileHandle).getFile();
    files.push({
      name,
      size: file.size,
      lastModified: file.lastModified,
      ownerId: getArchiveTemporaryOwnerId(name),
    });
  }
  return summarizeArchiveTemporaryUsage(files);
}
import type { ArchiveTemporaryManifest } from '../features/upload/domain/archive-manifest';
