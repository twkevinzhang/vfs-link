import type { ArchivePlan } from './archive-plan';
import { splitArchiveNames } from './archive-plan';
import type { UploadCandidate } from './upload-candidates';

const ZIP_MIME = 'application/zip';
export type ArchiveBuildProgress = {
  archiveIndex: number;
  archiveCount: number;
  archiveName: string;
  entryIndex: number;
  entryCount: number;
  progress: number;
};
export type BuiltArchive = { id: string; files: UploadCandidate[] };

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
  const writers: import('@zip.js/zip.js').BlobWriter[] = [];
  const makeWriter = () => {
    const writer = new zip.BlobWriter(ZIP_MIME);
    writers.push(writer);
    return writer;
  };
  let zipWriter: import('@zip.js/zip.js').ZipWriter<unknown>;
  if (options.splitSize > 0) {
    async function* outputs(): AsyncGenerator<
      import('@zip.js/zip.js').BlobWriter,
      boolean
    > {
      for (;;) yield makeWriter();
    }
    zipWriter = new zip.ZipWriter(
      new zip.SplitDataWriter(outputs(), options.splitSize),
      { zip64: true }
    );
  } else zipWriter = new zip.ZipWriter(makeWriter(), { zip64: true });
  for (const [entryIndex, entry] of plan.entries.entries()) {
    options.signal?.throwIfAborted();
    await zipWriter.add(entry.path, new zip.BlobReader(entry.file), {
      level: options.compressionLevel,
      password: options.password || undefined,
      encryptionStrength: 3,
      zip64: true,
      signal: options.signal,
      lastModDate: new Date(entry.file.lastModified),
      onprogress: (progress, total) =>
        options.onProgress?.({
          archiveIndex,
          archiveCount,
          archiveName: plan.name,
          entryIndex,
          entryCount: plan.entries.length,
          progress: total > 0 ? progress / total : 0,
        }),
    });
  }
  await zipWriter.close(undefined, { zip64: true });
  const blobs = await Promise.all(writers.map((writer) => writer.getData()));
  const names = splitArchiveNames(plan.name, blobs.length);
  return {
    id,
    files: blobs.map((blob, index) => ({
      file: new File([blob], names[index], {
        type: ZIP_MIME,
        lastModified: Date.now(),
      }),
      relativePath: names[index],
      selectionRoot: names[index],
      selectionRootKind: 'file',
    })),
  };
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
  const results: BuiltArchive[] = [];
  for (const [index, plan] of plans.entries()) {
    options.signal?.throwIfAborted();
    results.push(
      await buildOneArchive(plan, index, plans.length, zip, options)
    );
  }
  return results;
}
