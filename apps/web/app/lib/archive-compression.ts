import type { ArchivePlan } from './archive-plan';
import { splitArchiveNames } from './archive-file-names';
import type { UploadCandidate } from './folder-upload';
import {
  createArchiveTemporaryManifest,
  createArchiveTemporaryName,
  getArchiveOutputDirectory,
  removeArchiveTemporaryFiles,
  supportsArchiveTemporaryStorage,
  type ArchiveTemporaryManifest,
} from './archive-temporary-storage';

const ZIP_MIME = 'application/zip';

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

type OutputTarget = {
  writable: WritableStream;
  getBlob: () => Promise<Blob>;
  fileHandle?: FileSystemFileHandle;
  temporaryName?: string;
};

async function createOutputTarget(
  ownerId: string,
  zip: typeof import('@zip.js/zip.js')
): Promise<OutputTarget> {
  if (!supportsArchiveTemporaryStorage()) {
    const writer = new zip.BlobWriter(ZIP_MIME);
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
  zip: typeof import('@zip.js/zip.js'),
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
  let zipWriter: import('@zip.js/zip.js').ZipWriter<unknown>;
  if (options.splitSize > 0) {
    async function* outputs(): AsyncGenerator<OutputTarget, boolean> {
      for (;;) {
        const target = await createOutputTarget(id, zip);
        targets.push(target);
        yield target;
      }
      return true;
    }
    zipWriter = new zip.ZipWriter(
      new zip.SplitDataWriter(outputs(), options.splitSize),
      {
        zip64: true,
        createTempStream: supportsArchiveTemporaryStorage()
          ? zip.createOPFSTempStream({ directoryName: '.zip.js-temp' })
          : undefined,
      }
    );
  } else {
    const target = await createOutputTarget(id, zip);
    targets.push(target);
    zipWriter = new zip.ZipWriter(target.writable, {
      zip64: true,
      createTempStream: supportsArchiveTemporaryStorage()
        ? zip.createOPFSTempStream({ directoryName: '.zip.js-temp' })
        : undefined,
    });
  }

  try {
    for (const [entryIndex, entry] of plan.entries.entries()) {
      options.signal?.throwIfAborted();
      await zipWriter.add(entry.path, new zip.BlobReader(entry.file), {
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
  const zip = await import('@zip.js/zip.js');
  await assertArchiveStorageAvailable(plans);
  const results: BuiltArchive[] = [];
  for (const [index, plan] of plans.entries()) {
    options.signal?.throwIfAborted();
    results.push(
      await buildOneArchive(plan, index, plans.length, zip, options)
    );
  }
  return results;
}
