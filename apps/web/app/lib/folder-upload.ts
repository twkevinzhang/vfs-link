import type { ArchiveTemporaryManifest } from './archive-compression';

export type UploadCandidate = {
  file: File;
  /** A durable Chromium file handle, when the selection API exposes one. */
  fileHandle?: FileSystemFileHandle;
  /** Whether this source can be reopened after a page reload without reselecting it. */
  sourceHandlePersistence?: 'durable' | 'non-durable';
  relativePath: string;
  selectionRoot: string;
  selectionRootKind: 'file' | 'directory';
  archiveGroupId?: string;
  /** JSON-safe ownership metadata for generated OPFS archive output. */
  archiveTemporaryManifest?: ArchiveTemporaryManifest;
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
      sourceHandlePersistence: 'non-durable',
      relativePath,
      selectionRoot: directorySelection ? parts[0] : relativePath,
      selectionRootKind: directorySelection ? 'directory' : 'file',
    };
  });
}

type ModernDataTransferItem = DataTransferItem & {
  getAsFileSystemHandle?: () => Promise<FileSystemHandle | null>;
};

type ModernWindow = Window & {
  showOpenFilePicker?: (options?: {
    multiple?: boolean;
  }) => Promise<FileSystemFileHandle[]>;
  showDirectoryPicker?: () => Promise<FileSystemDirectoryHandle>;
};

async function walkHandle(
  handle: FileSystemHandle,
  parentPath: string,
  selectionRoot: string
): Promise<UploadCandidate[]> {
  if (handle.kind === 'file') {
    const fileHandle = handle as FileSystemFileHandle;
    const file = await fileHandle.getFile();
    return [
      {
        file,
        fileHandle,
        sourceHandlePersistence: 'durable',
        relativePath: cleanRelativePath(`${parentPath}/${file.name}`),
        selectionRoot,
        selectionRootKind: parentPath ? 'directory' : 'file',
      },
    ];
  }

  const directory = handle as FileSystemDirectoryHandle & {
    values: () => AsyncIterableIterator<FileSystemHandle>;
  };
  const directoryPath = cleanRelativePath(`${parentPath}/${directory.name}`);
  const nested: UploadCandidate[][] = [];
  for await (const child of directory.values()) {
    nested.push(await walkHandle(child, directoryPath, selectionRoot));
  }
  return nested.flat();
}

/** Uses durable handles in Chromium, and lets callers fall back to file inputs. */
export async function chooseFilesWithHandles() {
  const picker = (window as ModernWindow).showOpenFilePicker;
  if (!picker) return undefined;
  const handles = await picker({ multiple: true });
  return Promise.all(
    handles.map(async (fileHandle) => {
      const file = await fileHandle.getFile();
      return {
        file,
        fileHandle,
        sourceHandlePersistence: 'durable' as const,
        relativePath: cleanRelativePath(file.name),
        selectionRoot: file.name,
        selectionRootKind: 'file' as const,
      };
    })
  );
}

/** Uses a durable directory tree handle when the browser supports it. */
export async function chooseDirectoryWithHandles() {
  const picker = (window as ModernWindow).showDirectoryPicker;
  if (!picker) return undefined;
  const handle = await picker();
  return walkHandle(handle, '', handle.name);
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
        sourceHandlePersistence: 'non-durable',
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
  const items = Array.from(dataTransfer.items);
  if (items.length === 0) {
    return filesToUploadCandidates(dataTransfer.files);
  }

  const nested = await Promise.all(
    items.map(async (item) => {
      const handle = await (
        item as ModernDataTransferItem
      ).getAsFileSystemHandle?.();
      if (handle) return walkHandle(handle, '', handle.name);

      const entry = item.webkitGetAsEntry?.();
      if (entry) {
        return walkEntry(
          entry,
          '',
          entry.name,
          entry.isDirectory ? 'directory' : 'file'
        );
      }

      const file = item.getAsFile();
      return file ? filesToUploadCandidates([file]) : [];
    })
  );
  return nested.flat();
}
