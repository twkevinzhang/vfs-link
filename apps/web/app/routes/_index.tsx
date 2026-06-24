import {
  AlertCircle,
  Database,
  Download,
  File,
  Folder,
  HardDrive,
  RefreshCcw,
  Search,
  Server,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import type { MetaFunction } from 'react-router';

import { TreeView } from '../components/tree-view';
import { Alert } from '../components/ui/alert';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '../components/ui/card';
import { Input } from '../components/ui/input';
import { Skeleton } from '../components/ui/skeleton';
import { getDownloadUrl, getFiles, getStatus, getTree } from '../lib/api';
import { formatBytes, formatDate, normalizePath } from '../lib/format';
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

  const load = useCallback(async (path: string) => {
    setState((previous) => ({ ...previous, loading: true, error: undefined }));
    try {
      const [status, files, tree] = await Promise.all([
        getStatus(),
        getFiles(path),
        getTree(),
      ]);
      setState({ status, files, tree, loading: false });
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

  return (
    <main className="min-h-screen bg-background text-foreground">
      <div className="mx-auto flex min-h-screen w-full max-w-[1440px] flex-col gap-6 px-4 py-5 sm:px-6 lg:px-8">
        <header className="flex flex-col gap-4 border-b border-border pb-5 lg:flex-row lg:items-end lg:justify-between">
          <div className="grid gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="secondary">v3 local-first</Badge>
              <Badge variant="outline">
                {state.status?.storageDriver ?? 'local'}
              </Badge>
            </div>
            <div className="grid gap-1">
              <h1 className="text-2xl font-semibold tracking-normal sm:text-3xl">
                vfs-link file browser
              </h1>
              <p className="max-w-3xl text-sm leading-6 text-muted-foreground">
                Postgres metadata / local object store
              </p>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              onClick={() => void load(currentPath)}
              disabled={state.loading}
              title="重新整理"
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

        <section className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          <MetricCard
            icon={<Database aria-hidden="true" className="h-5 w-5" />}
            label="Files"
            value={state.status ? String(state.status.stats.fileCount) : '-'}
            detail={`${state.status?.stats.directoryCount ?? 0} folders`}
          />
          <MetricCard
            icon={<HardDrive aria-hidden="true" className="h-5 w-5" />}
            label="Logical bytes"
            value={formatBytes(state.status?.stats.totalBytes ?? 0)}
            detail="Postgres file records"
          />
          <MetricCard
            icon={<Server aria-hidden="true" className="h-5 w-5" />}
            label="Local objects"
            value={String(state.status?.stats.localObjectCount ?? 0)}
            detail={formatBytes(state.status?.stats.localObjectBytes ?? 0)}
          />
          <MetricCard
            icon={<Folder aria-hidden="true" className="h-5 w-5" />}
            label="Visible here"
            value={String(visibleEntries.length)}
            detail={formatBytes(totalVisibleBytes)}
          />
        </section>

        <section className="grid min-h-[620px] gap-4 lg:grid-cols-[320px_minmax(0,1fr)]">
          <aside className="rounded-lg border border-border bg-white p-4">
            <div className="mb-4 grid gap-1">
              <h2 className="text-sm font-semibold">Folders</h2>
              <p className="text-xs text-muted-foreground">
                {state.status?.storageRoot ?? 'LOCAL_STORAGE_ROOT'}
              </p>
            </div>
            <div className="max-h-[560px] overflow-auto pr-1">
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
                  onSelectPath={(path) => {
                    setCurrentPath(path);
                    setQuery('');
                  }}
                />
              )}
            </div>
          </aside>

          <section className="grid min-w-0 grid-rows-[auto_auto_minmax(0,1fr)] gap-4">
            <div className="flex flex-col gap-3 rounded-lg border border-border bg-white p-4 md:flex-row md:items-center md:justify-between">
              <div className="min-w-0">
                <Breadcrumbs
                  entries={state.files?.breadcrumbs ?? []}
                  currentPath={currentPath}
                  onSelectPath={setCurrentPath}
                />
              </div>
              <div className="relative min-w-[220px] md:w-[320px]">
                <Search
                  aria-hidden="true"
                  className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
                />
                <Input
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder="Search current folder"
                  className="pl-9"
                />
              </div>
            </div>

            <div className="overflow-hidden rounded-lg border border-border bg-white">
              {state.loading && !state.files ? (
                <LoadingTable />
              ) : visibleEntries.length === 0 ? (
                <EmptyState query={query} />
              ) : (
                <FileTable
                  entries={visibleEntries}
                  onOpenFolder={(path) => {
                    setCurrentPath(path);
                    setQuery('');
                  }}
                />
              )}
            </div>
          </section>
        </section>
      </div>
    </main>
  );
}

function MetricCard({
  icon,
  label,
  value,
  detail,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  detail: string;
}) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-2 text-muted-foreground">
          {icon}
          {label}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-1">
        <div className="truncate text-2xl font-semibold">{value}</div>
        <div className="truncate text-xs text-muted-foreground">{detail}</div>
      </CardContent>
    </Card>
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
            name: '/',
            kind: 'directory' as const,
            size: 0,
            updatedAt: '',
          },
        ];

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1 text-sm">
      {crumbs.map((entry, index) => {
        const path = normalizePath(entry.path);
        const isLast =
          path === normalizePath(currentPath) || index === crumbs.length - 1;
        return (
          <div
            key={`${entry.path}-${index}`}
            className="flex min-w-0 items-center gap-1"
          >
            <Button
              variant={isLast ? 'secondary' : 'ghost'}
              size="sm"
              className="max-w-[200px] truncate"
              onClick={() => onSelectPath(path)}
              title={path}
            >
              {entry.name || '/'}
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
  onOpenFolder,
}: {
  entries: FileEntry[];
  onOpenFolder: (path: string) => void;
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[760px] border-collapse text-sm">
        <thead>
          <tr className="border-b border-border bg-muted/70 text-left text-xs uppercase tracking-normal text-muted-foreground">
            <th className="px-4 py-3 font-semibold">Name</th>
            <th className="px-4 py-3 font-semibold">Type</th>
            <th className="px-4 py-3 text-right font-semibold">Size</th>
            <th className="px-4 py-3 font-semibold">Modified</th>
            <th className="px-4 py-3 text-right font-semibold">Action</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((entry) => {
            const isDirectory = entry.kind === 'directory';

            return (
              <tr
                key={entry.path}
                className="border-b border-border last:border-b-0"
              >
                <td className="px-4 py-3">
                  <button
                    type="button"
                    className="flex max-w-[360px] items-center gap-2 overflow-hidden text-left font-medium hover:text-accent disabled:hover:text-foreground"
                    onClick={() => isDirectory && onOpenFolder(entry.path)}
                    disabled={!isDirectory}
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
                    <a href={getDownloadUrl(entry.path)}>
                      <Button
                        variant="outline"
                        size="sm"
                        title={`Download ${entry.name}`}
                      >
                        <Download aria-hidden="true" className="h-4 w-4" />
                        Download
                      </Button>
                    </a>
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
