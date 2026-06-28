import {
  AlertCircle,
  Database,
  Download,
  File,
  Folder,
  HardDrive,
  Play,
  RefreshCcw,
  Search,
  Server,
  Share2,
} from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { MetaFunction } from 'react-router';

import { Alert } from '../components/ui/alert';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Skeleton } from '../components/ui/skeleton';
import { appPath } from '../lib/base-path';
import {
  createShareDraft,
  getDownloadUrl,
  getFiles,
  getPreviewUrl,
  getStatus,
} from '../lib/api';
import {
  formatBytes,
  formatDate,
  formatPathDisplayName,
  normalizePath,
} from '../lib/format';
import { cn } from '../lib/utils';
import {
  FileEntry,
  FilesResponse,
  Pagination,
  StatusResponse,
} from '../types/files';

export const meta: MetaFunction = () => [
  { title: 'vfs-link local browser' },
  {
    name: 'description',
    content: 'Local-first file browser for vfs-link FTP storage.',
  },
];

type LoadState = {
  status?: StatusResponse;
  files?: FilesResponse;
  loading: boolean;
  error?: string;
};

const FILE_PAGE_SIZE = 100;
const SEARCH_DEBOUNCE_MS = 250;
const TEXT_PREVIEW_LIMIT = 200_000;

type TextPreviewState =
  | { status: 'idle' | 'loading' }
  | { status: 'ready'; content: string; truncated: boolean }
  | { status: 'error'; message: string };

export default function Index() {
  const [currentPath, setCurrentPath] = useState('/');
  const [query, setQuery] = useState('');
  const [fileQuery, setFileQuery] = useState('');
  const [pageOffset, setPageOffset] = useState(0);
  const [state, setState] = useState<LoadState>({ loading: true });
  const [shareError, setShareError] = useState<string>();
  const [sharingPath, setSharingPath] = useState<string>();
  const [selectedFile, setSelectedFile] = useState<FileEntry>();
  const filesRequestRef = useRef(0);

  const loadStatus = useCallback(async () => {
    try {
      const status = await getStatus();
      setState((previous) => ({ ...previous, status }));
    } catch (error) {
      setState((previous) => ({
        ...previous,
        error: error instanceof Error ? error.message : 'Unable to load status',
      }));
    }
  }, []);

  const loadFiles = useCallback(
    async (path: string, searchQuery: string, offset: number) => {
      const requestId = filesRequestRef.current + 1;
      filesRequestRef.current = requestId;
      setState((previous) => ({
        ...previous,
        loading: true,
        error: undefined,
      }));
      try {
        const files = await getFiles(path, {
          query: searchQuery,
          limit: FILE_PAGE_SIZE,
          offset,
        });
        if (filesRequestRef.current !== requestId) {
          return;
        }
        setState((previous) => ({ ...previous, files, loading: false }));
      } catch (error) {
        if (filesRequestRef.current !== requestId) {
          return;
        }
        setState((previous) => ({
          ...previous,
          loading: false,
          error:
            error instanceof Error ? error.message : 'Unable to load files',
        }));
      }
    },
    []
  );

  const refresh = useCallback(() => {
    const nextOffset = 0;
    setPageOffset(nextOffset);
    void Promise.all([
      loadStatus(),
      loadFiles(currentPath, fileQuery, nextOffset),
    ]);
  }, [currentPath, fileQuery, loadFiles, loadStatus]);

  useEffect(() => {
    void loadStatus();
  }, [loadStatus]);

  useEffect(() => {
    const timeout = window.setTimeout(() => {
      setPageOffset(0);
      setFileQuery(query.trim());
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timeout);
  }, [query]);

  useEffect(() => {
    void loadFiles(currentPath, fileQuery, pageOffset);
  }, [currentPath, fileQuery, pageOffset, loadFiles]);

  const visibleEntries = state.files?.entries ?? [];
  const currentPagination = state.files?.pagination;
  const totalVisibleBytes = state.files?.visibleBytes ?? 0;

  const selectFile = useCallback((entry: FileEntry) => {
    setSelectedFile(entry);
    setShareError(undefined);
  }, []);

  const openFolder = useCallback((path: string) => {
    setCurrentPath(path);
    setSelectedFile(undefined);
    setQuery('');
    setFileQuery('');
    setPageOffset(0);
  }, []);

  const shareFile = useCallback(async (path: string) => {
    setShareError(undefined);
    setSharingPath(path);
    const popup = window.open('about:blank', '_blank');
    try {
      const draft = await createShareDraft(path);
      const sharePath = `/share/${encodeURIComponent(draft.id)}`;
      if (popup) {
        popup.opener = null;
        popup.location.replace(appPath(sharePath));
      } else {
        window.location.href = appPath(sharePath);
      }
    } catch (error) {
      popup?.close();
      setShareError(
        error instanceof Error ? error.message : 'Unable to create share'
      );
    } finally {
      setSharingPath(undefined);
    }
  }, []);

  return (
    <main className="min-h-screen bg-background text-foreground lg:h-screen lg:min-h-0 lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1440px] flex-col gap-3 px-4 py-4 sm:px-6 lg:h-full lg:min-h-0 lg:px-8">
        <header className="flex flex-col gap-3 border-b border-border pb-3 xl:flex-row xl:items-center xl:justify-between">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <h1 className="mr-1 text-2xl font-semibold tracking-normal sm:text-3xl">
              vfs-link file browser
            </h1>
            <HeaderMetricBadge
              icon={<Database aria-hidden="true" className="h-3.5 w-3.5" />}
              label="Files"
              value={state.status ? String(state.status.stats.fileCount) : '-'}
              detail={`${state.status?.stats.directoryCount ?? 0} folders`}
            />
            <HeaderMetricBadge
              icon={<HardDrive aria-hidden="true" className="h-3.5 w-3.5" />}
              label="Logical bytes"
              shortLabel="Bytes"
              value={formatBytes(state.status?.stats.totalBytes ?? 0)}
              detail="Postgres file records"
            />
            <HeaderMetricBadge
              icon={<Server aria-hidden="true" className="h-3.5 w-3.5" />}
              label="Local objects"
              shortLabel="Objects"
              value={String(state.status?.stats.localObjectCount ?? 0)}
              detail={formatBytes(state.status?.stats.localObjectBytes ?? 0)}
            />
          </div>
          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-end xl:shrink-0">
            <div className="relative w-full md:w-[320px]">
              <Search
                aria-hidden="true"
                className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
              />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search current folder"
                className="h-9 pl-8"
              />
            </div>
            <VisibleMetric
              value={String(currentPagination?.total ?? 0)}
              detail={formatBytes(totalVisibleBytes)}
            />
            <Button
              variant="outline"
              onClick={refresh}
              disabled={state.loading}
              title="重新整理"
              className="h-9 w-full px-3 md:w-auto"
            >
              <RefreshCcw aria-hidden="true" className="h-4 w-4" />
              Refresh
            </Button>
          </div>
        </header>

        {state.error && (
          <Alert className="border-destructive/35 bg-white text-destructive">
            <div className="flex items-start gap-3">
              <AlertCircle
                aria-hidden="true"
                className="mt-0.5 h-5 w-5 shrink-0"
              />
              <div className="grid gap-1">
                <p className="font-semibold">API unavailable</p>
                <p className="text-sm text-foreground">{state.error}</p>
              </div>
            </div>
          </Alert>
        )}

        {shareError && (
          <Alert className="border-destructive/35 bg-white text-destructive">
            <div className="flex items-start gap-3">
              <AlertCircle
                aria-hidden="true"
                className="mt-0.5 h-5 w-5 shrink-0"
              />
              <div className="grid gap-1">
                <p className="font-semibold">Share unavailable</p>
                <p className="text-sm text-foreground">{shareError}</p>
              </div>
            </div>
          </Alert>
        )}

        <section className="flex min-w-0 flex-col gap-4 lg:min-h-0 lg:flex-1">
          <div className="overflow-x-auto rounded-lg border border-border bg-white p-4">
            <div className="min-w-max">
              <Breadcrumbs
                entries={state.files?.breadcrumbs ?? []}
                currentPath={currentPath}
                onSelectPath={openFolder}
              />
            </div>
          </div>

          <section className="grid gap-4 lg:min-h-0 lg:flex-1 xl:grid-cols-[minmax(0,1fr)_360px] 2xl:grid-cols-[minmax(0,1fr)_420px]">
            <div className="min-h-0 overflow-hidden rounded-lg border border-border bg-white lg:flex-1">
              {state.loading && !state.files ? (
                <LoadingTable />
              ) : visibleEntries.length === 0 ? (
                <EmptyState query={query} />
              ) : (
                <FileTable
                  entries={visibleEntries}
                  pagination={currentPagination}
                  visibleBytes={totalVisibleBytes}
                  sharingPath={sharingPath}
                  selectedPath={selectedFile?.path}
                  onPageChange={setPageOffset}
                  onOpenFolder={openFolder}
                  onSelectFile={selectFile}
                  onShareFile={shareFile}
                />
              )}
            </div>
            <FileInspector
              file={selectedFile}
              sharingPath={sharingPath}
              onClear={() => setSelectedFile(undefined)}
              onShareFile={shareFile}
            />
          </section>
        </section>
      </div>
    </main>
  );
}

function HeaderMetricBadge({
  icon,
  label,
  shortLabel,
  value,
  detail,
}: {
  icon: React.ReactNode;
  label: string;
  shortLabel?: string;
  value: string;
  detail: string;
}) {
  return (
    <Badge
      variant="outline"
      className="h-8 max-w-full gap-1.5 bg-white px-2.5 py-1.5 text-foreground shadow-sm"
      title={`${label}: ${value} (${detail})`}
    >
      <span className="shrink-0 text-muted-foreground">{icon}</span>
      <span className="truncate text-muted-foreground">
        {shortLabel ?? label}
      </span>
      <span className="shrink-0 font-semibold">{value}</span>
      <span className="hidden truncate font-normal text-muted-foreground 2xl:inline">
        {detail}
      </span>
    </Badge>
  );
}

function VisibleMetric({ value, detail }: { value: string; detail: string }) {
  return (
    <Badge
      variant="outline"
      className="h-9 max-w-full gap-1.5 bg-white px-2.5 py-1.5 text-foreground shadow-sm"
      title={`Visible here: ${value} (${detail})`}
    >
      <Folder
        aria-hidden="true"
        className="h-3.5 w-3.5 shrink-0 text-muted-foreground"
      />
      <span className="truncate text-muted-foreground">Visible here</span>
      <span className="shrink-0 font-semibold">{value}</span>
      <span className="shrink-0 font-normal text-muted-foreground">
        {detail}
      </span>
    </Badge>
  );
}

function Breadcrumbs({
  entries,
  currentPath,
  onSelectPath,
}: {
  entries: FileEntry[];
  currentPath: string;
  onSelectPath: (path: string) => void;
}) {
  const crumbs =
    entries.length > 0
      ? entries
      : [
          {
            path: '/',
            name: formatPathDisplayName('/'),
            kind: 'directory' as const,
            size: 0,
            updatedAt: '',
          },
        ];

  return (
    <div className="flex min-w-max flex-nowrap items-center gap-1 text-sm">
      {crumbs.map((entry, index) => {
        const path = normalizePath(entry.path);
        const label = formatPathDisplayName(path, entry.name);
        const isLast =
          path === normalizePath(currentPath) || index === crumbs.length - 1;
        return (
          <div
            key={`${entry.path}-${index}`}
            className="flex shrink-0 items-center gap-1"
          >
            <Button
              variant={isLast ? 'secondary' : 'ghost'}
              size="sm"
              className="max-w-none shrink-0 whitespace-nowrap"
              onClick={() => onSelectPath(path)}
              title={path === '/' ? label : path}
            >
              {label}
            </Button>
            {!isLast && <span className="text-muted-foreground">/</span>}
          </div>
        );
      })}
    </div>
  );
}

function FileTable({
  entries,
  pagination,
  visibleBytes,
  sharingPath,
  selectedPath,
  onPageChange,
  onOpenFolder,
  onSelectFile,
  onShareFile,
}: {
  entries: FileEntry[];
  pagination?: Pagination;
  visibleBytes: number;
  sharingPath?: string;
  selectedPath?: string;
  onPageChange: (offset: number) => void;
  onOpenFolder: (path: string) => void;
  onSelectFile: (entry: FileEntry) => void;
  onShareFile: (path: string) => void;
}) {
  const limit = pagination?.limit ?? FILE_PAGE_SIZE;
  const offset = pagination?.offset ?? 0;
  const total = pagination?.total ?? entries.length;
  const pageStart = total === 0 ? 0 : offset + 1;
  const pageEnd = Math.min(offset + entries.length, total);
  const pageNumber = Math.floor(offset / limit) + 1;
  const totalPages = Math.max(1, Math.ceil(total / limit));

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full min-w-[760px] border-collapse text-sm">
          <thead>
            <tr className="border-b border-border bg-muted/70 text-left text-xs uppercase tracking-normal text-muted-foreground">
              <th className="px-4 py-3 font-semibold">Name</th>
              <th className="px-4 py-3 font-semibold">Type</th>
              <th className="px-4 py-3 text-right font-semibold">Size</th>
              <th className="px-4 py-3 font-semibold">Modified</th>
              <th className="px-4 py-3 text-right font-semibold">Actions</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => {
              const isDirectory = entry.kind === 'directory';
              const isSelected = entry.path === selectedPath;

              return (
                <tr
                  key={entry.path}
                  className={cn(
                    'border-b border-border last:border-b-0',
                    isSelected && 'bg-muted/50'
                  )}
                >
                  <td className="px-4 py-3">
                    <button
                      type="button"
                      className="flex max-w-[360px] items-center gap-2 overflow-hidden text-left font-medium hover:text-accent"
                      onClick={() =>
                        isDirectory
                          ? onOpenFolder(entry.path)
                          : onSelectFile(entry)
                      }
                      title={entry.path}
                    >
                      {isDirectory ? (
                        <Folder
                          aria-hidden="true"
                          className="h-4 w-4 shrink-0 text-[#11615a]"
                        />
                      ) : (
                        <File
                          aria-hidden="true"
                          className="h-4 w-4 shrink-0 text-[#276c93]"
                        />
                      )}
                      <span className="truncate">{entry.name}</span>
                    </button>
                  </td>
                  <td className="px-4 py-3">
                    <Badge variant={isDirectory ? 'secondary' : 'outline'}>
                      {entry.kind}
                    </Badge>
                  </td>
                  <td className="px-4 py-3 text-right tabular-nums">
                    {isDirectory ? '-' : formatBytes(entry.size)}
                  </td>
                  <td className="px-4 py-3 text-muted-foreground">
                    {formatDate(entry.updatedAt)}
                  </td>
                  <td className="px-4 py-3 text-right">
                    {isDirectory ? (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => onOpenFolder(entry.path)}
                      >
                        <Folder aria-hidden="true" className="h-4 w-4" />
                        Open
                      </Button>
                    ) : (
                      <div className="flex justify-end gap-2">
                        <Button
                          variant="outline"
                          size="icon"
                          aria-label={`Share ${entry.name}`}
                          onClick={() => onShareFile(entry.path)}
                          disabled={sharingPath === entry.path}
                          title={
                            sharingPath === entry.path
                              ? `Sharing ${entry.name}`
                              : `Share ${entry.name}`
                          }
                          className="h-8 w-8"
                        >
                          <Share2 aria-hidden="true" className="h-4 w-4" />
                        </Button>
                        <a href={getDownloadUrl(entry.path)}>
                          <Button
                            variant="outline"
                            size="icon"
                            aria-label={`Download ${entry.name}`}
                            title={`Download ${entry.name}`}
                            className="h-8 w-8"
                          >
                            <Download aria-hidden="true" className="h-4 w-4" />
                          </Button>
                        </a>
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="flex flex-col gap-2 border-t border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground md:flex-row md:items-center md:justify-between">
        <span>
          Showing {pageStart}-{pageEnd} of {total}
          {pagination?.query ? ` matching "${pagination.query}"` : ''} ·{' '}
          {formatBytes(visibleBytes)}
        </span>
        <div className="flex items-center gap-2">
          <span className="tabular-nums">
            Page {pageNumber} / {totalPages}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onPageChange(Math.max(0, offset - limit))}
            disabled={!pagination?.hasPrev}
          >
            Previous
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onPageChange(offset + limit)}
            disabled={!pagination?.hasNext}
          >
            Next
          </Button>
        </div>
      </div>
    </div>
  );
}

function FileInspector({
  file,
  sharingPath,
  onClear,
  onShareFile,
}: {
  file?: FileEntry;
  sharingPath?: string;
  onClear: () => void;
  onShareFile: (path: string) => void;
}) {
  if (!file) {
    return (
      <aside className="min-h-[360px] overflow-hidden rounded-lg border border-border bg-white lg:min-h-0">
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
    <aside className="min-h-[480px] overflow-hidden rounded-lg border border-border bg-white lg:min-h-0">
      <div className="flex h-full min-h-0 flex-col">
        <div className="grid gap-4 border-b border-border p-4">
          <div className="min-w-0">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <Badge variant="outline">active file</Badge>
              <Badge variant="secondary">{formatBytes(file.size)}</Badge>
            </div>
            <h2 className="truncate text-base font-semibold" title={file.path}>
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
            <a href={getPreviewUrl(file.path)} target="_blank" rel="noreferrer">
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

function EmptyState({ query }: { query: string }) {
  return (
    <div className="grid min-h-[320px] place-items-center p-8 text-center">
      <div className="grid max-w-sm gap-3">
        <Folder
          aria-hidden="true"
          className="mx-auto h-10 w-10 text-muted-foreground"
        />
        <div className="grid gap-1">
          <h2 className="text-base font-semibold">
            {query.trim() ? 'No matching entries' : 'This folder is empty'}
          </h2>
          <p className="text-sm leading-6 text-muted-foreground">
            No entries are available for the current view.
          </p>
        </div>
      </div>
    </div>
  );
}

function LoadingTable() {
  return (
    <div className="grid gap-3 p-4">
      {Array.from({ length: 6 }).map((_, index) => (
        <div
          key={index}
          className="grid grid-cols-[minmax(0,1fr)_120px_120px] gap-4"
        >
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ))}
    </div>
  );
}
