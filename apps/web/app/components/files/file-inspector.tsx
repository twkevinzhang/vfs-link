import {
  Download,
  File,
  FolderInput,
  Pencil,
  Play,
  Share2,
  Trash2,
  X,
} from 'lucide-react';
import { useEffect, useState } from 'react';

import { getDownloadUrl, getPreviewUrl, getThumbnailUrl } from '../../lib/api';
import { formatBytes, formatDate } from '../../lib/format';
import { type FileEntry } from '../../types/files';
import { Badge } from '../ui/badge';
import { Button } from '../ui/button';
import { Skeleton } from '../ui/skeleton';

const TEXT_PREVIEW_LIMIT = 200_000;

type TextPreviewState =
  | { status: 'idle' | 'loading' }
  | { status: 'ready'; content: string; truncated: boolean }
  | { status: 'error'; message: string };

export function FileInspector({
  file,
  sharingPath,
  onClear,
  onShareFile,
  onMove,
  onRename,
  onTrash,
}: {
  file?: FileEntry;
  sharingPath?: string;
  onClear: () => void;
  onShareFile: (path: string) => void;
  onMove: (file: FileEntry) => void;
  onRename: (file: FileEntry) => void;
  onTrash: (file: FileEntry) => void;
}) {
  if (!file) {
    return (
      <aside className="hidden min-h-[360px] overflow-hidden rounded-lg border border-border bg-white xl:block xl:min-h-0">
        <div className="grid h-full min-h-[360px] place-items-center p-6 text-center">
          <div className="grid max-w-xs gap-3">
            <File
              aria-hidden="true"
              className="mx-auto h-10 w-10 text-muted-foreground"
            />
            <div className="grid gap-1">
              <h2 className="text-base font-semibold">No active file</h2>
            </div>
          </div>
        </div>
      </aside>
    );
  }

  return (
    <>
      <aside className="hidden min-h-[480px] overflow-hidden rounded-lg border border-border bg-white xl:block xl:min-h-0">
        <div className="flex h-full min-h-0 flex-col">
          <div className="grid gap-4 border-b border-border p-4">
            <div className="min-w-0">
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <Badge variant="outline">active file</Badge>
                <Badge variant="secondary">{formatBytes(file.size)}</Badge>
              </div>
              <h2
                className="truncate text-base font-semibold"
                title={file.path}
              >
                {file.name}
              </h2>
              <p className="mt-1 break-all text-xs text-muted-foreground">
                {file.path}
              </p>
            </div>
            <dl className="grid grid-cols-[88px_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
              <dt className="text-muted-foreground">Type</dt>
              <dd>file</dd>
              <dt className="text-muted-foreground">Size</dt>
              <dd>{formatBytes(file.size)}</dd>
              <dt className="text-muted-foreground">Modified</dt>
              <dd>{formatDate(file.updatedAt)}</dd>
            </dl>
            <div className="flex flex-wrap gap-2">
              <a
                href={getPreviewUrl(file.path)}
                target="_blank"
                rel="noreferrer"
              >
                <Button variant="outline" size="sm">
                  <Play aria-hidden="true" className="h-4 w-4" />
                  Open
                </Button>
              </a>
              <Button
                variant="outline"
                size="icon"
                aria-label={`Share ${file.name}`}
                onClick={() => onShareFile(file.path)}
                disabled={sharingPath === file.path}
                title={
                  sharingPath === file.path
                    ? `Sharing ${file.name}`
                    : `Share ${file.name}`
                }
                className="h-8 w-8"
              >
                <Share2 aria-hidden="true" className="h-4 w-4" />
              </Button>
              <a href={getDownloadUrl(file.path)}>
                <Button
                  variant="outline"
                  size="icon"
                  aria-label={`Download ${file.name}`}
                  title={`Download ${file.name}`}
                  className="h-8 w-8"
                >
                  <Download aria-hidden="true" className="h-4 w-4" />
                </Button>
              </a>
              <Button variant="outline" size="sm" onClick={() => onMove(file)}>
                <FolderInput className="h-4 w-4" />
                Move
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onRename(file)}
              >
                <Pencil className="h-4 w-4" />
                Rename
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => onTrash(file)}
              >
                <Trash2 className="h-4 w-4" />
                Trash
              </Button>
              <Button variant="ghost" size="sm" onClick={onClear}>
                Clear
              </Button>
            </div>
          </div>
          <div className="flex min-h-0 flex-1 flex-col">
            <div className="border-b border-border px-4 py-3">
              <h3 className="text-sm font-semibold">Preview</h3>
            </div>
            <FilePreview file={file} />
          </div>
        </div>
      </aside>

      <button
        type="button"
        aria-label="Close file preview"
        className="fixed inset-0 z-40 bg-black/25 xl:hidden"
        onClick={onClear}
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={`Preview ${file.name}`}
        className="fixed inset-x-0 bottom-0 z-50 max-h-[86dvh] overflow-hidden rounded-t-2xl border border-border bg-white shadow-2xl xl:hidden"
      >
        <div className="flex max-h-[86dvh] flex-col">
          <div className="grid gap-4 border-b border-border p-4">
            <div className="flex items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="mb-2 flex flex-wrap items-center gap-2">
                  <Badge variant="outline">active file</Badge>
                  <Badge variant="secondary">{formatBytes(file.size)}</Badge>
                </div>
                <h2
                  className="break-words text-base font-semibold"
                  title={file.path}
                >
                  {file.name}
                </h2>
                <p className="mt-1 break-all text-xs text-muted-foreground">
                  {file.path}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon"
                aria-label="Close file preview"
                onClick={onClear}
                className="h-9 w-9 shrink-0"
              >
                <X aria-hidden="true" className="h-4 w-4" />
              </Button>
            </div>
            <dl className="grid grid-cols-[76px_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
              <dt className="text-muted-foreground">Type</dt>
              <dd>file</dd>
              <dt className="text-muted-foreground">Size</dt>
              <dd>{formatBytes(file.size)}</dd>
              <dt className="text-muted-foreground">Modified</dt>
              <dd>{formatDate(file.updatedAt)}</dd>
            </dl>
            <div className="flex flex-wrap gap-2">
              <a
                href={getPreviewUrl(file.path)}
                target="_blank"
                rel="noreferrer"
              >
                <Button variant="outline" size="sm">
                  <Play aria-hidden="true" className="h-4 w-4" />
                  Open
                </Button>
              </a>
              <Button
                variant="outline"
                size="icon"
                aria-label={`Share ${file.name}`}
                onClick={() => onShareFile(file.path)}
                disabled={sharingPath === file.path}
                title={
                  sharingPath === file.path
                    ? `Sharing ${file.name}`
                    : `Share ${file.name}`
                }
                className="h-9 w-9"
              >
                <Share2 aria-hidden="true" className="h-4 w-4" />
              </Button>
              <a href={getDownloadUrl(file.path)}>
                <Button
                  variant="outline"
                  size="icon"
                  aria-label={`Download ${file.name}`}
                  title={`Download ${file.name}`}
                  className="h-9 w-9"
                >
                  <Download aria-hidden="true" className="h-4 w-4" />
                </Button>
              </a>
              <Button variant="outline" size="sm" onClick={() => onMove(file)}>
                <FolderInput className="h-4 w-4" />
                Move
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onRename(file)}
              >
                <Pencil className="h-4 w-4" />
                Rename
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => onTrash(file)}
              >
                <Trash2 className="h-4 w-4" />
                Trash
              </Button>
            </div>
          </div>
          <div className="flex min-h-0 flex-1 flex-col">
            <div className="border-b border-border px-4 py-3">
              <h3 className="text-sm font-semibold">Preview</h3>
            </div>
            <div className="flex min-h-[260px] flex-1 flex-col overflow-hidden">
              <FilePreview file={file} />
            </div>
          </div>
        </div>
      </aside>
    </>
  );
}

function FilePreview({ file }: { file: FileEntry }) {
  const previewUrl = getPreviewUrl(file.path);
  const previewKind = getPreviewKind(file);
  const [textPreview, setTextPreview] = useState<TextPreviewState>({
    status: 'idle',
  });

  useEffect(() => {
    if (previewKind !== 'text') {
      setTextPreview({ status: 'idle' });
      return;
    }

    const controller = new AbortController();
    setTextPreview({ status: 'loading' });

    fetch(previewUrl, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) {
          throw new Error(`${response.status} ${response.statusText}`);
        }
        return response.text();
      })
      .then((content) => {
        const formatted = formatTextPreview(file, content);
        setTextPreview({
          status: 'ready',
          content: formatted.slice(0, TEXT_PREVIEW_LIMIT),
          truncated: formatted.length > TEXT_PREVIEW_LIMIT,
        });
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        setTextPreview({
          status: 'error',
          message:
            error instanceof Error ? error.message : 'Unable to load preview',
        });
      });

    return () => controller.abort();
  }, [file, previewKind, previewUrl]);

  if (file.thumbnail) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center overflow-auto bg-muted/20 p-4">
        <img
          src={getThumbnailUrl(file.thumbnail.id)}
          alt={`${file.name} 的壓縮檔縮圖`}
          className="max-h-full max-w-full rounded-md object-contain shadow-sm"
        />
      </div>
    );
  }

  if (previewKind === 'image') {
    return (
      <div className="grid min-h-0 flex-1 place-items-center overflow-auto bg-muted/20 p-4">
        <img
          src={previewUrl}
          alt={file.name}
          className="max-h-full max-w-full rounded-md object-contain"
        />
      </div>
    );
  }

  if (previewKind === 'video') {
    return (
      <div className="grid min-h-0 flex-1 place-items-center overflow-auto bg-muted/20 p-4">
        <video
          src={previewUrl}
          controls
          preload="metadata"
          className="max-h-full max-w-full rounded-md bg-black"
        />
      </div>
    );
  }

  if (previewKind === 'text') {
    if (textPreview.status === 'loading') {
      return (
        <div className="grid gap-3 p-4">
          <Skeleton className="h-5 w-2/3" />
          <Skeleton className="h-5" />
          <Skeleton className="h-5 w-5/6" />
        </div>
      );
    }

    if (textPreview.status === 'error') {
      return (
        <div className="grid min-h-0 flex-1 place-items-center p-6 text-center text-sm text-muted-foreground">
          Preview unavailable: {textPreview.message}
        </div>
      );
    }

    if (textPreview.status === 'ready') {
      return (
        <div className="min-h-0 flex-1 overflow-auto bg-[#111827] p-4 text-xs leading-5 text-[#e5e7eb]">
          <pre className="whitespace-pre-wrap break-words font-mono">
            {textPreview.content}
          </pre>
          {textPreview.truncated && (
            <p className="mt-4 border-t border-white/15 pt-3 text-[#cbd5e1]">
              Preview truncated at {formatBytes(TEXT_PREVIEW_LIMIT)}.
            </p>
          )}
        </div>
      );
    }
  }

  return (
    <div className="grid min-h-0 flex-1 place-items-center p-6 text-center">
      <div className="grid max-w-xs gap-3">
        <File
          aria-hidden="true"
          className="mx-auto h-10 w-10 text-muted-foreground"
        />
        <div className="grid gap-1">
          <h3 className="text-sm font-semibold">No preview available</h3>
        </div>
      </div>
    </div>
  );
}

function getPreviewKind(file: FileEntry) {
  const extension = fileExtension(file.name);
  if (
    ['avif', 'bmp', 'gif', 'jpeg', 'jpg', 'png', 'svg', 'webp'].includes(
      extension
    )
  ) {
    return 'image';
  }

  if (['m4v', 'mov', 'mp4', 'ogg', 'ogv', 'webm'].includes(extension)) {
    return 'video';
  }

  if (
    [
      'c',
      'conf',
      'cpp',
      'cs',
      'css',
      'csv',
      'go',
      'h',
      'html',
      'java',
      'js',
      'json',
      'jsx',
      'log',
      'md',
      'py',
      'rs',
      'sh',
      'sql',
      'toml',
      'ts',
      'tsx',
      'txt',
      'xml',
      'yaml',
      'yml',
    ].includes(extension)
  ) {
    return 'text';
  }

  return 'unsupported';
}

function formatTextPreview(file: FileEntry, content: string) {
  if (fileExtension(file.name) !== 'json') {
    return content;
  }

  try {
    return JSON.stringify(JSON.parse(content), null, 2);
  } catch {
    return content;
  }
}

function fileExtension(name: string) {
  const index = name.lastIndexOf('.');
  if (index < 0 || index === name.length - 1) {
    return '';
  }
  return name.slice(index + 1).toLowerCase();
}
