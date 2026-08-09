import { File, Folder, RotateCcw, Trash2 } from 'lucide-react';

import type { FilesPresentationDependencies } from '../files-presentation-contracts';
import { type FileViewMode } from '../file-view-mode';
import {
  formatBytes,
  formatDate,
} from '../../../../shared/presentation/format';
import { cn } from '../../../../shared/presentation/utils';
import { type FileEntry } from '../../domain/files';
import type { Pagination } from '../../application/files-results';
import { FileActionMenu } from './file-actions';
import { Badge } from '../../../../shared/presentation/ui/badge';
import { Button } from '../../../../shared/presentation/ui/button';
import { Checkbox } from '../../../../shared/presentation/ui/checkbox';

type FileEntryVisualVariant = 'table' | 'grid' | 'mobile';

const fileEntryVisualClasses: Record<
  FileEntryVisualVariant,
  { directory: string; file: string; thumbnail: string }
> = {
  table: {
    directory: 'h-4 w-4 shrink-0 text-[#11615a]',
    file: 'h-4 w-4 shrink-0 text-[#276c93]',
    thumbnail: 'h-7 w-7 shrink-0 rounded object-cover',
  },
  grid: {
    directory: 'h-16 w-16 text-[#11615a] sm:h-20 sm:w-20',
    file: 'h-14 w-14 text-[#276c93] sm:h-16 sm:w-16',
    thumbnail: 'h-full w-full object-contain',
  },
  mobile: {
    directory: 'mt-0.5 h-5 w-5 shrink-0 text-[#11615a]',
    file: 'mt-0.5 h-5 w-5 shrink-0 text-[#276c93]',
    thumbnail: 'h-10 w-10 shrink-0 rounded object-cover',
  },
};

function FileEntryVisual({
  entry,
  variant,
  getThumbnailUrl,
}: {
  entry: FileEntry;
  variant: FileEntryVisualVariant;
  getThumbnailUrl: FilesPresentationDependencies['getThumbnailUrl'];
}) {
  const classes = fileEntryVisualClasses[variant];
  if (entry.kind === 'directory') {
    return <Folder aria-hidden="true" className={classes.directory} />;
  }
  if (entry.thumbnail) {
    return (
      <img
        src={getThumbnailUrl(entry.thumbnail.id)}
        alt=""
        className={classes.thumbnail}
      />
    );
  }
  return <File aria-hidden="true" className={classes.file} />;
}

function formatFileEntrySize(entry: FileEntry) {
  return formatBytes(
    entry.kind === 'directory' ? entry.folderSummary?.bytes ?? 0 : entry.size
  );
}

function formatFileEntryDate(entry: FileEntry, trashView = false) {
  return formatDate(
    trashView ? entry.trashedAt ?? entry.updatedAt : entry.updatedAt
  );
}

export function FileTable({
  entries,
  viewMode,
  pagination,
  pageSize,
  visibleBytes,
  sharingPath,
  selectedPaths,
  trashView,
  entryKey,
  onPageChange,
  onOpenFolder,
  onSelectFile,
  onSelect,
  onMove,
  onRename,
  onTrash,
  onRestore,
  onPermanentDelete,
  onShareFile,
  dependencies,
}: {
  entries: FileEntry[];
  viewMode: FileViewMode;
  pagination?: Pagination;
  pageSize: number;
  visibleBytes: number;
  sharingPath?: string;
  selectedPaths: Set<string>;
  trashView: boolean;
  entryKey: (entry: FileEntry) => string;
  onPageChange: (offset: number) => void;
  onOpenFolder: (path: string) => void;
  onSelectFile: (entry: FileEntry) => void;
  onSelect: (
    entry: FileEntry,
    options: { toggle?: boolean; range?: boolean }
  ) => void;
  onMove: (entry: FileEntry) => void;
  onRename: (entry: FileEntry) => void;
  onTrash: (entry: FileEntry) => void;
  onRestore: (entry: FileEntry) => void;
  onPermanentDelete: (entry: FileEntry) => void;
  onShareFile: (path: string) => void;
  dependencies: Pick<
    FilesPresentationDependencies,
    'getDownloadUrl' | 'getThumbnailUrl'
  >;
}) {
  const limit = pagination?.limit ?? pageSize;
  const offset = pagination?.offset ?? 0;
  const total = pagination?.total ?? entries.length;
  const pageStart = total === 0 ? 0 : offset + 1;
  const pageEnd = Math.min(offset + entries.length, total);
  const pageNumber = Math.floor(offset / limit) + 1;
  const totalPages = Math.max(1, Math.ceil(total / limit));

  return (
    <div className="flex h-full min-h-0 flex-col">
      {viewMode === 'grid' && !trashView ? (
        <FileGrid
          entries={entries}
          sharingPath={sharingPath}
          selectedPaths={selectedPaths}
          entryKey={entryKey}
          onOpenFolder={onOpenFolder}
          onSelectFile={onSelectFile}
          onSelect={onSelect}
          onMove={onMove}
          onRename={onRename}
          onTrash={onTrash}
          onShareFile={onShareFile}
          dependencies={dependencies}
        />
      ) : (
        <>
          <div className="md:hidden">
            <MobileFileList
              entries={entries}
              sharingPath={sharingPath}
              trashView={trashView}
              entryKey={entryKey}
              onOpenFolder={onOpenFolder}
              onSelectFile={onSelectFile}
              onMove={onMove}
              onRename={onRename}
              onTrash={onTrash}
              onRestore={onRestore}
              onPermanentDelete={onPermanentDelete}
              onShareFile={onShareFile}
              dependencies={dependencies}
            />
          </div>
          <div className="hidden min-h-0 flex-1 overflow-auto md:block">
            <table className="w-full min-w-[820px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/70 text-left text-xs uppercase tracking-normal text-muted-foreground">
                  <th className="w-12 px-4 py-3">
                    <span className="sr-only">Select</span>
                  </th>
                  <th className="px-4 py-3 font-semibold">Name</th>
                  <th className="px-4 py-3 font-semibold">Type</th>
                  <th className="px-4 py-3 text-right font-semibold">Size</th>
                  <th className="px-4 py-3 font-semibold">
                    {trashView ? 'Trashed' : 'Modified'}
                  </th>
                  <th className="px-4 py-3 text-right font-semibold">
                    Actions
                  </th>
                </tr>
              </thead>
              <tbody>
                {entries.map((entry) => {
                  const isDirectory = entry.kind === 'directory';
                  const selectionKey = entryKey(entry);
                  const isSelected = selectedPaths.has(selectionKey);

                  return (
                    <tr
                      key={selectionKey}
                      className={cn(
                        'border-b border-border last:border-b-0 hover:bg-muted/30',
                        isSelected && 'bg-muted/50'
                      )}
                      onClick={(event) =>
                        onSelect(entry, {
                          toggle: event.metaKey || event.ctrlKey,
                          range: event.shiftKey,
                        })
                      }
                      onDoubleClick={() => {
                        if (!trashView) {
                          if (isDirectory) onOpenFolder(entry.path);
                          else onSelectFile(entry);
                        }
                      }}
                    >
                      <td
                        className="px-4 py-3"
                        onClick={(event) => event.stopPropagation()}
                      >
                        <Checkbox
                          checked={isSelected}
                          onChange={(event) =>
                            onSelect(entry, {
                              toggle: true,
                              range:
                                event.nativeEvent instanceof MouseEvent &&
                                event.nativeEvent.shiftKey,
                            })
                          }
                          aria-label={`Select ${entry.name}`}
                        />
                      </td>
                      <td className="px-4 py-3">
                        <div
                          className="flex max-w-[360px] items-center gap-2 overflow-hidden text-left font-medium"
                          title={entry.path}
                        >
                          <FileEntryVisual
                            entry={entry}
                            variant="table"
                            getThumbnailUrl={dependencies.getThumbnailUrl}
                          />
                          <span className="truncate">{entry.name}</span>
                        </div>
                      </td>
                      <td className="px-4 py-3">
                        <Badge variant={isDirectory ? 'secondary' : 'outline'}>
                          {entry.kind}
                        </Badge>
                      </td>
                      <td className="px-4 py-3 text-right tabular-nums">
                        {formatFileEntrySize(entry)}
                      </td>
                      <td className="px-4 py-3 text-muted-foreground">
                        {formatFileEntryDate(entry, trashView)}
                      </td>
                      <td
                        className="px-4 py-3 text-right"
                        onClick={(event) => event.stopPropagation()}
                      >
                        {trashView ? (
                          <div className="flex justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => onRestore(entry)}
                            >
                              <RotateCcw className="h-4 w-4" />
                              Restore
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-8 w-8 text-destructive"
                              aria-label={`Delete ${entry.name} permanently`}
                              onClick={() => onPermanentDelete(entry)}
                            >
                              <Trash2 className="h-4 w-4" />
                            </Button>
                          </div>
                        ) : (
                          <FileActionMenu
                            entry={entry}
                            dependencies={dependencies}
                            sharing={sharingPath === entry.path}
                            onOpen={() =>
                              isDirectory
                                ? onOpenFolder(entry.path)
                                : onSelectFile(entry)
                            }
                            onShare={() => onShareFile(entry.path)}
                            onRename={() => onRename(entry)}
                            onMove={() => onMove(entry)}
                            onTrash={() => onTrash(entry)}
                          />
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
      <div className="flex flex-col gap-2 border-t border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground md:flex-row md:items-center md:justify-between">
        <span>
          Showing {pageStart}-{pageEnd} of {total}
          {pagination?.query ? ` matching "${pagination.query}"` : ''} ·{' '}
          {pagination?.query ? 'Matching direct files' : 'Direct files'}{' '}
          {formatBytes(visibleBytes)}
        </span>
        {pagination && (
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
        )}
      </div>
    </div>
  );
}

export function FileGrid({
  entries,
  sharingPath,
  selectedPaths,
  entryKey,
  onOpenFolder,
  onSelectFile,
  onSelect,
  onMove,
  onRename,
  onTrash,
  onShareFile,
  dependencies,
}: {
  entries: FileEntry[];
  sharingPath?: string;
  selectedPaths: Set<string>;
  entryKey: (entry: FileEntry) => string;
  onOpenFolder: (path: string) => void;
  onSelectFile: (entry: FileEntry) => void;
  onSelect: (
    entry: FileEntry,
    options: { toggle?: boolean; range?: boolean }
  ) => void;
  onMove: (entry: FileEntry) => void;
  onRename: (entry: FileEntry) => void;
  onTrash: (entry: FileEntry) => void;
  onShareFile: (path: string) => void;
  dependencies: Pick<
    FilesPresentationDependencies,
    'getDownloadUrl' | 'getThumbnailUrl'
  >;
}) {
  const openEntry = (entry: FileEntry) => {
    if (entry.kind === 'directory') onOpenFolder(entry.path);
    else onSelectFile(entry);
  };

  return (
    <div className="min-h-0 flex-1 overflow-auto p-3 sm:p-4">
      <div className="grid grid-cols-2 gap-3 sm:gap-4 md:grid-cols-3 lg:grid-cols-4 xl:grid-cols-3 2xl:grid-cols-4">
        {entries.map((entry) => {
          const selectionKey = entryKey(entry);
          const isSelected = selectedPaths.has(selectionKey);

          return (
            <article
              key={selectionKey}
              className={cn(
                'group relative min-w-0 overflow-hidden rounded-lg border border-border bg-white transition-colors hover:bg-muted/20',
                isSelected && 'border-accent bg-accent/5 ring-2 ring-accent/30'
              )}
            >
              <div
                className="absolute left-2 top-2 z-10 rounded bg-white/90 p-1 shadow-sm backdrop-blur-sm"
                onClick={(event) => event.stopPropagation()}
              >
                <Checkbox
                  checked={isSelected}
                  onChange={(event) =>
                    onSelect(entry, {
                      toggle: true,
                      range:
                        event.nativeEvent instanceof MouseEvent &&
                        event.nativeEvent.shiftKey,
                    })
                  }
                  aria-label={`Select ${entry.name}`}
                />
              </div>

              <div
                className="absolute right-2 top-2 z-10 rounded bg-white/90 shadow-sm backdrop-blur-sm"
                onClick={(event) => event.stopPropagation()}
              >
                <FileActionMenu
                  entry={entry}
                  dependencies={dependencies}
                  sharing={sharingPath === entry.path}
                  onOpen={() => openEntry(entry)}
                  onShare={() => onShareFile(entry.path)}
                  onRename={() => onRename(entry)}
                  onMove={() => onMove(entry)}
                  onTrash={() => onTrash(entry)}
                />
              </div>

              <div
                role="button"
                tabIndex={0}
                className="cursor-default outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
                title={entry.path}
                onClick={(event) => {
                  if (window.matchMedia('(max-width: 767px)').matches) {
                    openEntry(entry);
                    return;
                  }
                  onSelect(entry, {
                    toggle: event.metaKey || event.ctrlKey,
                    range: event.shiftKey,
                  });
                }}
                onDoubleClick={() => {
                  if (!window.matchMedia('(max-width: 767px)').matches) {
                    openEntry(entry);
                  }
                }}
                onKeyDown={(event) => {
                  if (event.key !== 'Enter' && event.key !== ' ') return;
                  event.preventDefault();
                  if (window.matchMedia('(max-width: 767px)').matches) {
                    openEntry(entry);
                  } else {
                    onSelect(entry, {});
                  }
                }}
              >
                <div className="flex aspect-[4/3] items-center justify-center overflow-hidden bg-muted/25">
                  <FileEntryVisual
                    entry={entry}
                    variant="grid"
                    getThumbnailUrl={dependencies.getThumbnailUrl}
                  />
                </div>

                <div className="grid min-w-0 gap-1.5 p-3">
                  <h3
                    className="truncate text-sm font-medium"
                    title={entry.name}
                  >
                    {entry.name}
                  </h3>
                  <div className="flex min-w-0 items-center justify-between gap-2 text-xs text-muted-foreground">
                    <span className="shrink-0 tabular-nums">
                      {formatFileEntrySize(entry)}
                    </span>
                    <span
                      className="truncate"
                      title={formatFileEntryDate(entry)}
                    >
                      {formatFileEntryDate(entry)}
                    </span>
                  </div>
                </div>
              </div>
            </article>
          );
        })}
      </div>
    </div>
  );
}

export function MobileFileList({
  entries,
  sharingPath,
  trashView,
  entryKey,
  onOpenFolder,
  onSelectFile,
  onMove,
  onRename,
  onTrash,
  onRestore,
  onPermanentDelete,
  onShareFile,
  dependencies,
}: {
  entries: FileEntry[];
  sharingPath?: string;
  trashView: boolean;
  entryKey: (entry: FileEntry) => string;
  onOpenFolder: (path: string) => void;
  onSelectFile: (entry: FileEntry) => void;
  onMove: (entry: FileEntry) => void;
  onRename: (entry: FileEntry) => void;
  onTrash: (entry: FileEntry) => void;
  onRestore: (entry: FileEntry) => void;
  onPermanentDelete: (entry: FileEntry) => void;
  onShareFile: (path: string) => void;
  dependencies: Pick<
    FilesPresentationDependencies,
    'getDownloadUrl' | 'getThumbnailUrl'
  >;
}) {
  return (
    <div className="divide-y divide-border">
      {entries.map((entry) => {
        const isDirectory = entry.kind === 'directory';

        return (
          <div key={entryKey(entry)} className="grid gap-3 p-4">
            <button
              type="button"
              className="flex min-w-0 items-start gap-3 text-left"
              onClick={() =>
                !trashView &&
                (isDirectory ? onOpenFolder(entry.path) : onSelectFile(entry))
              }
              title={entry.path}
            >
              <FileEntryVisual
                entry={entry}
                variant="mobile"
                getThumbnailUrl={dependencies.getThumbnailUrl}
              />
              <span className="min-w-0 flex-1 break-words font-medium leading-6">
                {entry.name}
              </span>
            </button>

            <div className="ml-8 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <Badge variant={isDirectory ? 'secondary' : 'outline'}>
                {entry.kind}
              </Badge>
              <span className="tabular-nums">{formatFileEntrySize(entry)}</span>
              <span>{formatFileEntryDate(entry, trashView)}</span>
            </div>

            <div className="ml-8 flex flex-wrap gap-2">
              {trashView ? (
                <>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onRestore(entry)}
                  >
                    <RotateCcw className="h-4 w-4" />
                    Restore
                  </Button>
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => onPermanentDelete(entry)}
                  >
                    Delete permanently
                  </Button>
                </>
              ) : (
                <FileActionMenu
                  entry={entry}
                  dependencies={dependencies}
                  sharing={sharingPath === entry.path}
                  onOpen={() =>
                    isDirectory ? onOpenFolder(entry.path) : onSelectFile(entry)
                  }
                  onShare={() => onShareFile(entry.path)}
                  onRename={() => onRename(entry)}
                  onMove={() => onMove(entry)}
                  onTrash={() => onTrash(entry)}
                />
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
