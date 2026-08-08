import {
  BlobReader,
  BlobWriter,
  SplitDataWriter,
  ZipWriter,
  createOPFSTempStream,
} from '@zip.js/zip.js';

import type { ArchivePlan } from './archive-plan';
import { splitArchiveNames } from './archive-plan';
import type { UploadCandidate } from './folder-upload';

const ZIP_MIME = 'application/zip';
const OPFS_DIRECTORY = '_vfs-link-archive-output';
const TEMPORARY_FILE_PREFIX = 'vfs-archive-v1-';
const TEMPORARY_FILE_SEPARATOR = '--';
const ARCHIVE_TEMPORARY_MANIFEST_VERSION = 1 as const;

export type ArchiveBuildProgress = {
  archiveIndex: number;
  archiveCount: number;
  archiveName: string;
  entryIndex: number;
  entryCount: number;
  progress: number;
};

export type BuiltArchive = {
  id: string;
  files: UploadCandidate[];
  temporaryNames: string[];
  temporaryManifest?: ArchiveTemporaryManifest;
};

export type ArchiveTemporaryManifest = {
  version: typeof ARCHIVE_TEMPORARY_MANIFEST_VERSION;
  ownerId: string;
  createdAt: number;
  files: Array<{
    name: string;
    size: number;
  }>;
};

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

type OutputTarget = {
  writable: WritableStream;
  getBlob: () => Promise<Blob>;
  fileHandle?: FileSystemFileHandle;
  temporaryName?: string;
};

function supportsOPFS() {
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

async function getArchiveOutputDirectory(create: boolean) {
  if (!supportsOPFS()) return undefined;
  const root = await navigator.storage.getDirectory();
  try {
    return await root.getDirectoryHandle(OPFS_DIRECTORY, { create });
  } catch (error) {
    if (!create && isNotFoundError(error)) return undefined;
    throw error;
  }
}

async function createOutputTarget(ownerId: string): Promise<OutputTarget> {
  if (!supportsOPFS()) {
    const writer = new BlobWriter(ZIP_MIME);
    return { writable: writer.writable, getBlob: () => writer.getData() };
  }
  const directory = await getArchiveOutputDirectory(true);
  if (!directory) throw new Error('OPFS archive directory is unavailable');
  const temporaryName = createArchiveTemporaryName(ownerId);
  const handle = await directory.getFileHandle(temporaryName, { create: true });
  const writable = await handle.createWritable();
  return {
    writable,
    fileHandle: handle,
    temporaryName,
    getBlob: () => handle.getFile(),
  };
}

export async function removeArchiveTemporaryFiles(names: string[]) {
  if (!supportsOPFS() || names.length === 0) return;
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

export async function assertArchiveStorageAvailable(plans: ArchivePlan[]) {
  if (typeof navigator === 'undefined' || !navigator.storage?.estimate) return;
  const estimate = await navigator.storage.estimate();
  if (estimate.quota === undefined || estimate.usage === undefined) return;
  const sourceBytes = plans.reduce(
    (total, plan) =>
      total + plan.entries.reduce((sum, entry) => sum + entry.file.size, 0),
    0
  );
  const required = Math.ceil(sourceBytes * 1.05) + plans.length * 64 * 1024;
  if (estimate.quota - estimate.usage < required) {
    throw new Error(
      '瀏覽器儲存空間不足，無法安全建立壓縮檔。請釋放空間或縮小選取內容。'
    );
  }
}

async function buildOneArchive(
  plan: ArchivePlan,
  archiveIndex: number,
  archiveCount: number,
  options: {
    compressionLevel: number;
    splitSize: number;
    password: string;
    signal?: AbortSignal;
    onProgress?: (progress: ArchiveBuildProgress) => void;
  }
): Promise<BuiltArchive> {
  const id = crypto.randomUUID();
  const createdAt = Date.now();
  const targets: OutputTarget[] = [];
  let zipWriter: ZipWriter<unknown>;
  if (options.splitSize > 0) {
    async function* outputs(): AsyncGenerator<OutputTarget, boolean> {
      for (;;) {
        const target = await createOutputTarget(id);
        targets.push(target);
        yield target;
      }
      return true;
    }
    zipWriter = new ZipWriter(
      new SplitDataWriter(outputs(), options.splitSize),
      {
        zip64: true,
        createTempStream: supportsOPFS()
          ? createOPFSTempStream({ directoryName: '.zip.js-temp' })
          : undefined,
      }
    );
  } else {
    const target = await createOutputTarget(id);
    targets.push(target);
    zipWriter = new ZipWriter(target.writable, {
      zip64: true,
      createTempStream: supportsOPFS()
        ? createOPFSTempStream({ directoryName: '.zip.js-temp' })
        : undefined,
    });
  }

  try {
    for (const [entryIndex, entry] of plan.entries.entries()) {
      options.signal?.throwIfAborted();
      await zipWriter.add(entry.path, new BlobReader(entry.file), {
        level: options.compressionLevel,
        password: options.password || undefined,
        encryptionStrength: 3,
        zip64: true,
        signal: options.signal,
        lastModDate: new Date(entry.file.lastModified),
        onprogress: (progress, total) => {
          options.onProgress?.({
            archiveIndex,
            archiveCount,
            archiveName: plan.name,
            entryIndex,
            entryCount: plan.entries.length,
            progress: total > 0 ? progress / total : 0,
          });
        },
      });
    }
    await zipWriter.close(undefined, { zip64: true });
    const names = splitArchiveNames(plan.name, targets.length);
    const blobs = await Promise.all(targets.map((target) => target.getBlob()));
    const temporaryFiles = targets.flatMap((target, index) =>
      target.temporaryName
        ? [{ name: target.temporaryName, size: blobs[index].size }]
        : []
    );
    return {
      id,
      files: blobs.map((blob, index) => ({
        file: new File([blob], names[index], {
          type: ZIP_MIME,
          lastModified: Date.now(),
        }),
        fileHandle: targets[index].fileHandle,
        sourceHandlePersistence: targets[index].fileHandle
          ? 'durable'
          : 'non-durable',
        relativePath: names[index],
        selectionRoot: names[index],
        selectionRootKind: 'file',
      })),
      temporaryNames: targets
        .map((target) => target.temporaryName)
        .filter((name): name is string => Boolean(name)),
      temporaryManifest:
        temporaryFiles.length > 0
          ? createArchiveTemporaryManifest(id, temporaryFiles, createdAt)
          : undefined,
    };
  } catch (error) {
    await removeArchiveTemporaryFiles(
      targets
        .map((target) => target.temporaryName)
        .filter((name): name is string => Boolean(name))
    );
    throw error;
  }
}

export async function buildArchives(
  plans: ArchivePlan[],
  options: {
    compressionLevel: number;
    splitSize: number;
    password: string;
    signal?: AbortSignal;
    onProgress?: (progress: ArchiveBuildProgress) => void;
  }
) {
  await assertArchiveStorageAvailable(plans);
  const results: BuiltArchive[] = [];
  for (const [index, plan] of plans.entries()) {
    options.signal?.throwIfAborted();
    results.push(await buildOneArchive(plan, index, plans.length, options));
  }
  return results;
}
