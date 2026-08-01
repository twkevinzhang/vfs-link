import type { UploadCandidate } from './folder-upload';

export type ArchiveOptions = {
  archiveName: string;
  compressionLevel: number;
  splitSize: number;
  password: string;
  oneArchivePerItem: boolean;
  preserveExtension: boolean;
  recurseFolders: boolean;
};

export type ArchiveEntry = {
  file: File;
  path: string;
};

export type ArchivePlan = {
  id: string;
  name: string;
  entries: ArchiveEntry[];
  thumbnailCandidates: UploadCandidate[];
};

function cleanArchiveName(value: string) {
  const name = value.trim().replace(/[\\/]/g, '-');
  return /\.zip$/i.test(name) ? name : `${name || 'upload'}.zip`;
}

function stripExtension(name: string) {
  const index = name.lastIndexOf('.');
  return index > 0 ? name.slice(0, index) : name;
}

function uniqueArchiveName(baseName: string, used: Set<string>) {
  const normalized = cleanArchiveName(baseName);
  const stem = normalized.slice(0, -4);
  let candidate = normalized;
  let suffix = 2;
  while (used.has(candidate.toLocaleLowerCase())) {
    candidate = `${stem} (${suffix}).zip`;
    suffix += 1;
  }
  used.add(candidate.toLocaleLowerCase());
  return candidate;
}

function isImage(candidate: UploadCandidate) {
  return (
    candidate.file.type.startsWith('image/') ||
    /\.(avif|bmp|gif|jpe?g|png|webp)$/i.test(candidate.file.name)
  );
}

function makePlan(
  name: string,
  candidates: UploadCandidate[],
  entryPath: (candidate: UploadCandidate) => string
): ArchivePlan {
  const sorted = [...candidates].sort((left, right) =>
    left.relativePath.localeCompare(right.relativePath)
  );
  return {
    id: `${name}-${sorted.map((item) => item.relativePath).join('|')}`,
    name,
    entries: sorted.map((candidate) => ({
      file: candidate.file,
      path: entryPath(candidate),
    })),
    thumbnailCandidates: sorted.filter(isImage),
  };
}

/** Implements WinRAR's selected-item grouping semantics for ZIP output. */
export function buildArchivePlans(
  candidates: UploadCandidate[],
  options: ArchiveOptions
): ArchivePlan[] {
  if (candidates.length === 0) return [];
  if (!options.oneArchivePerItem) {
    return [
      makePlan(
        cleanArchiveName(options.archiveName),
        candidates,
        (item) => item.relativePath
      ),
    ];
  }

  const usedNames = new Set<string>();
  if (options.recurseFolders) {
    return [...candidates]
      .sort((left, right) =>
        left.relativePath.localeCompare(right.relativePath)
      )
      .map((candidate) => {
        const sourceName = candidate.file.name;
        const stem = options.preserveExtension
          ? sourceName
          : stripExtension(sourceName);
        const name = uniqueArchiveName(stem, usedNames);
        return makePlan(name, [candidate], () => sourceName);
      });
  }

  const groups = new Map<string, UploadCandidate[]>();
  for (const candidate of candidates) {
    const key = `${candidate.selectionRootKind}:${candidate.selectionRoot}`;
    groups.set(key, [...(groups.get(key) ?? []), candidate]);
  }
  return [...groups.values()]
    .sort((left, right) =>
      left[0].selectionRoot.localeCompare(right[0].selectionRoot)
    )
    .map((group) => {
      const root = group[0].selectionRoot;
      const stem =
        group[0].selectionRootKind === 'file' && !options.preserveExtension
          ? stripExtension(root)
          : root;
      const name = uniqueArchiveName(stem, usedNames);
      return makePlan(name, group, (candidate) => {
        if (candidate.selectionRootKind === 'directory') {
          return candidate.relativePath;
        }
        return candidate.file.name;
      });
    });
}

export function splitArchiveNames(name: string, partCount: number) {
  if (partCount <= 1) return [cleanArchiveName(name)];
  const stem = cleanArchiveName(name).slice(0, -4);
  return Array.from({ length: partCount }, (_, index) =>
    index === partCount - 1
      ? `${stem}.zip`
      : `${stem}.z${String(index + 1).padStart(2, '0')}`
  );
}
