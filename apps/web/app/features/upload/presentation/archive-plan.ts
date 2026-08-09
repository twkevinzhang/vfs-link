import type { UploadCandidate } from './upload-candidates';

export type ArchiveOptions = {
  archiveName: string;
  compressionLevel: number;
  splitSize: number;
  password: string;
  oneArchivePerItem: boolean;
  preserveExtension: boolean;
  recurseFolders: boolean;
};
export type ArchivePlan = {
  id: string;
  name: string;
  entries: Array<{ file: File; path: string }>;
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
  while (used.has(candidate.toLowerCase()))
    candidate = `${stem} (${suffix++}).zip`;
  used.add(candidate.toLowerCase());
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
  const sorted = [...candidates].sort((a, b) =>
    a.relativePath.localeCompare(b.relativePath)
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

export function buildArchivePlans(
  candidates: UploadCandidate[],
  options: ArchiveOptions
): ArchivePlan[] {
  if (!candidates.length) return [];
  if (!options.oneArchivePerItem)
    return [
      makePlan(
        cleanArchiveName(options.archiveName),
        candidates,
        (item) => item.relativePath
      ),
    ];
  const used = new Set<string>();
  if (options.recurseFolders)
    return [...candidates]
      .sort((a, b) => a.relativePath.localeCompare(b.relativePath))
      .map((candidate) =>
        makePlan(
          uniqueArchiveName(
            options.preserveExtension
              ? candidate.file.name
              : stripExtension(candidate.file.name),
            used
          ),
          [candidate],
          () => candidate.file.name
        )
      );
  const groups = new Map<string, UploadCandidate[]>();
  for (const candidate of candidates) {
    const key = `${candidate.selectionRootKind}:${candidate.selectionRoot}`;
    groups.set(key, [...(groups.get(key) ?? []), candidate]);
  }
  return [...groups.values()]
    .sort((a, b) => a[0].selectionRoot.localeCompare(b[0].selectionRoot))
    .map((group) => {
      const root = group[0].selectionRoot;
      const stem =
        group[0].selectionRootKind === 'file' && !options.preserveExtension
          ? stripExtension(root)
          : root;
      return makePlan(uniqueArchiveName(stem, used), group, (candidate) =>
        candidate.selectionRootKind === 'directory'
          ? candidate.relativePath
          : candidate.file.name
      );
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
