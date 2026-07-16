export type UploadCandidate = {
  file: File;
  relativePath: string;
};

function cleanRelativePath(value: string) {
  return value
    .replaceAll('\\', '/')
    .split('/')
    .filter((part) => part && part !== '.' && part !== '..')
    .join('/');
}

export function filesToUploadCandidates(
  files: FileList | File[]
): UploadCandidate[] {
  return Array.from(files).map((file) => ({
    file,
    relativePath: cleanRelativePath(file.webkitRelativePath || file.name),
  }));
}

function readFileEntry(entry: FileSystemFileEntry) {
  return new Promise<File>((resolve, reject) => entry.file(resolve, reject));
}

async function readDirectoryEntries(entry: FileSystemDirectoryEntry) {
  const reader = entry.createReader();
  const entries: FileSystemEntry[] = [];
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) =>
      reader.readEntries(resolve, reject)
    );
    if (batch.length === 0) break;
    entries.push(...batch);
  }
  return entries;
}

async function walkEntry(
  entry: FileSystemEntry,
  parentPath: string
): Promise<UploadCandidate[]> {
  if (entry.isFile) {
    const file = await readFileEntry(entry as FileSystemFileEntry);
    return [
      {
        file,
        relativePath: cleanRelativePath(`${parentPath}/${file.name}`),
      },
    ];
  }
  if (!entry.isDirectory) return [];

  const directoryPath = cleanRelativePath(`${parentPath}/${entry.name}`);
  const children = await readDirectoryEntries(
    entry as FileSystemDirectoryEntry
  );
  const nested = await Promise.all(
    children.map((child) => walkEntry(child, directoryPath))
  );
  return nested.flat();
}

export async function collectDroppedFiles(
  dataTransfer: DataTransfer
): Promise<UploadCandidate[]> {
  const entries = Array.from(dataTransfer.items)
    .map((item) => item.webkitGetAsEntry?.())
    .filter((entry): entry is FileSystemEntry => Boolean(entry));

  if (entries.length === 0) {
    return filesToUploadCandidates(dataTransfer.files);
  }
  const nested = await Promise.all(
    entries.map((entry) => walkEntry(entry, ''))
  );
  return nested.flat();
}
