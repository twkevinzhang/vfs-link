export type UploadCandidate = {
  file: File;
  relativePath: string;
  selectionRoot: string;
  selectionRootKind: 'file' | 'directory';
  archiveGroupId?: string;
};

export type PreparedArchiveBatch = {
  id: string;
  candidates: UploadCandidate[];
  thumbnail?: { blob: Blob; width: number; height: number };
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
  return Array.from(files).map((file) => {
    const relativePath = cleanRelativePath(
      file.webkitRelativePath || file.name
    );
    const parts = relativePath.split('/');
    const directorySelection = parts.length > 1;
    return {
      file,
      relativePath,
      selectionRoot: directorySelection ? parts[0] : relativePath,
      selectionRootKind: directorySelection ? 'directory' : 'file',
    };
  });
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
  parentPath: string,
  selectionRoot: string,
  selectionRootKind: UploadCandidate['selectionRootKind']
): Promise<UploadCandidate[]> {
  if (entry.isFile) {
    const file = await readFileEntry(entry as FileSystemFileEntry);
    return [
      {
        file,
        relativePath: cleanRelativePath(`${parentPath}/${file.name}`),
        selectionRoot,
        selectionRootKind,
      },
    ];
  }
  if (!entry.isDirectory) return [];
  const directoryPath = cleanRelativePath(`${parentPath}/${entry.name}`);
  const children = await readDirectoryEntries(
    entry as FileSystemDirectoryEntry
  );
  return (
    await Promise.all(
      children.map((child) =>
        walkEntry(child, directoryPath, selectionRoot, selectionRootKind)
      )
    )
  ).flat();
}

export async function collectDroppedFiles(
  dataTransfer: DataTransfer
): Promise<UploadCandidate[]> {
  const items = Array.from(dataTransfer.items);
  if (items.length === 0) return filesToUploadCandidates(dataTransfer.files);
  return (
    await Promise.all(
      items.map(async (item) => {
        const entry = item.webkitGetAsEntry?.();
        if (entry)
          return walkEntry(
            entry,
            '',
            entry.name,
            entry.isDirectory ? 'directory' : 'file'
          );
        const file = item.getAsFile();
        return file ? filesToUploadCandidates([file]) : [];
      })
    )
  ).flat();
}
