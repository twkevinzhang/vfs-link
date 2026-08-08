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

async function createOutputTarget(): Promise<OutputTarget> {
  if (!supportsOPFS()) {
    const writer = new BlobWriter(ZIP_MIME);
    return { writable: writer.writable, getBlob: () => writer.getData() };
  }
  const root = await navigator.storage.getDirectory();
  const directory = await root.getDirectoryHandle(OPFS_DIRECTORY, {
    create: true,
  });
  const temporaryName = crypto.randomUUID();
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
  const root = await navigator.storage.getDirectory();
  const directory = await root.getDirectoryHandle(OPFS_DIRECTORY, {
    create: true,
  });
  await Promise.all(
    names.map((name) => directory.removeEntry(name).catch(() => undefined))
  );
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
  const targets: OutputTarget[] = [];
  let zipWriter: ZipWriter<unknown>;
  if (options.splitSize > 0) {
    async function* outputs(): AsyncGenerator<OutputTarget, boolean> {
      for (;;) {
        const target = await createOutputTarget();
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
    const target = await createOutputTarget();
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
    return {
      id: crypto.randomUUID(),
      files: blobs.map((blob, index) => ({
        file: new File([blob], names[index], {
          type: ZIP_MIME,
          lastModified: Date.now(),
        }),
        fileHandle: targets[index].fileHandle,
        relativePath: names[index],
        selectionRoot: names[index],
        selectionRootKind: 'file',
      })),
      temporaryNames: targets
        .map((target) => target.temporaryName)
        .filter((name): name is string => Boolean(name)),
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
