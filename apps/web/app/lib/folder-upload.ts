export type UploadCandidate = {
  file: File;
  relativePath: string;
  selectionRoot: string;
  selectionRootKind: 'file' | 'directory';
  archiveGroupId?: string;
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
  const nested = await Promise.all(
    children.map((child) =>
      walkEntry(child, directoryPath, selectionRoot, selectionRootKind)
    )
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
    entries.map((entry) =>
      walkEntry(entry, '', entry.name, entry.isDirectory ? 'directory' : 'file')
    )
  );
  return nested.flat();
}
