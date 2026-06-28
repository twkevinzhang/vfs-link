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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { MetaFunction } from 'react-router';

import { TreeView } from '../components/tree-view';
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
  getStatus,
  getTree,
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
  StatusResponse,
  TreeNode,
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
  tree?: TreeNode;
  loading: boolean;
  error?: string;
};

export default function Index() {
  const [currentPath, setCurrentPath] = useState('/');
  const [query, setQuery] = useState('');
  const [state, setState] = useState<LoadState>({ loading: true });
  const [shareError, setShareError] = useState<string>();
  const [sharingPath, setSharingPath] = useState<string>();
  const [selectedFile, setSelectedFile] = useState<FileEntry>();
  const loadedTreeRef = useRef(false);

  const load = useCallback(async (path: string, refreshTree = false) => {
    setState((previous) => ({ ...previous, loading: true, error: undefined }));
    try {
      if (refreshTree || !loadedTreeRef.current) {
        const [status, files, tree] = await Promise.all([
          getStatus(),
          getFiles(path),
          getTree(),
        ]);
        loadedTreeRef.current = true;
        setState({ status, files, tree, loading: false });
        return;
      }

      const files = await getFiles(path);
      setState((previous) => ({ ...previous, files, loading: false }));
    } catch (error) {
      setState((previous) => ({
        ...previous,
        loading: false,
        error: error instanceof Error ? error.message : 'Unable to load files',
      }));
    }
  }, []);

  useEffect(() => {
    void load(currentPath);
  }, [currentPath, load]);

  const visibleEntries = useMemo(() => {
    const entries = state.files?.entries ?? [];
    const normalizedQuery = query.trim().toLowerCase();
    if (!normalizedQuery) {
      return entries;
    }
    return entries.filter((entry) =>
      `${entry.name} ${entry.path}`.toLowerCase().includes(normalizedQuery)
    );
  }, [query, state.files?.entries]);

  const totalVisibleBytes = visibleEntries.reduce(
    (sum, entry) => sum + (entry.kind === 'directory' ? 0 : entry.size),
    0
  );

  const selectFile = useCallback((entry: FileEntry) => {
    setSelectedFile(entry);
    setShareError(undefined);
    const parentPath = parentDirectory(entry.path);
    setCurrentPath(parentPath);
    setQuery('');
  }, []);

  const openFolder = useCallback((path: string) => {
    setCurrentPath(path);
    setSelectedFile(undefined);
    setQuery('');
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
              value={String(visibleEntries.length)}
              detail={formatBytes(totalVisibleBytes)}
            />
            <Button
              variant="outline"
              onClick={() => void load(currentPath, true)}
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

        <section className="grid gap-4 lg:min-h-0 lg:flex-1 lg:grid-cols-[320px_minmax(0,1fr)]">
          <aside className="min-h-0 overflow-auto rounded-lg border border-border bg-white p-4">
            <div className="mb-4 grid min-w-max gap-1">
              <h2 className="text-sm font-semibold">Folders</h2>
              <p className="text-xs text-muted-foreground">
                {state.status?.storageRoot ?? 'LOCAL_STORAGE_ROOT'}
              </p>
            </div>
            <div className="min-w-max pr-1">
              {state.loading && !state.tree ? (
                <div className="grid gap-2">
                  <Skeleton className="h-8" />
                  <Skeleton className="h-8 w-4/5" />
                  <Skeleton className="h-8 w-3/5" />
                </div>
              ) : (
                <TreeView
                  node={state.tree}
                  currentPath={currentPath}
                  selectedFilePath={selectedFile?.path}
                  onSelectPath={openFolder}
                  onSelectFile={(node) =>
                    selectFile({
                      path: node.path,
                      name: node.name,
                      kind: 'file',
                      size: node.size ?? 0,
                      updatedAt: node.updatedAt ?? '',
                    })
                  }
                />
              )}
            </div>
          </aside>

          <section className="flex min-w-0 flex-col gap-4 lg:min-h-0">
            <div className="overflow-x-auto rounded-lg border border-border bg-white p-4">
              <div className="min-w-max">
                <Breadcrumbs
                  entries={state.files?.breadcrumbs ?? []}
                  currentPath={currentPath}
                  onSelectPath={openFolder}
                />
              </div>
            </div>

            {selectedFile && (
              <SelectedFilePanel
                file={selectedFile}
                sharingPath={sharingPath}
                onClear={() => setSelectedFile(undefined)}
                onShareFile={shareFile}
              />
            )}

            <div className="min-h-0 overflow-hidden rounded-lg border border-border bg-white lg:flex-1">
              {state.loading && !state.files ? (
                <LoadingTable />
              ) : visibleEntries.length === 0 ? (
                <EmptyState query={query} />
              ) : (
                <FileTable
                  entries={visibleEntries}
                  sharingPath={sharingPath}
                  selectedPath={selectedFile?.path}
                  onOpenFolder={openFolder}
                  onSelectFile={selectFile}
                  onShareFile={shareFile}
                />
              )}
            </div>
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
  sharingPath,
  selectedPath,
  onOpenFolder,
  onSelectFile,
  onShareFile,
}: {
  entries: FileEntry[];
  sharingPath?: string;
  selectedPath?: string;
  onOpenFolder: (path: string) => void;
  onSelectFile: (entry: FileEntry) => void;
  onShareFile: (path: string) => void;
}) {
  return (
    <div className="h-full min-h-0 overflow-auto">
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
  );
}

function SelectedFilePanel({
  file,
  sharingPath,
  onClear,
  onShareFile,
}: {
  file: FileEntry;
  sharingPath?: string;
  onClear: () => void;
  onShareFile: (path: string) => void;
}) {
  return (
    <section className="rounded-lg border border-border bg-white p-4">
      <div className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
        <div className="min-w-0">
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <Badge variant="outline">selected file</Badge>
            <Badge variant="secondary">{formatBytes(file.size)}</Badge>
          </div>
          <h2 className="truncate text-base font-semibold" title={file.path}>
            {file.name}
          </h2>
          <p className="mt-1 break-all text-xs text-muted-foreground">
            {file.path}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            Modified {formatDate(file.updatedAt)}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <a href={getDownloadUrl(file.path)} target="_blank" rel="noreferrer">
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
    </section>
  );
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

function parentDirectory(filePath: string) {
  const normalized = normalizePath(filePath);
  const index = normalized.lastIndexOf('/');
  if (index <= 0) {
    return '/';
  }
  return normalized.slice(0, index);
}
