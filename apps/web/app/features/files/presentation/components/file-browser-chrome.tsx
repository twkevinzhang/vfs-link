import { Check, Copy, Folder, Trash2 } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

import {
  formatPathDisplayName,
  normalizePath,
} from '../../../../shared/presentation/format';
import { type FileEntry } from '../../domain/files';
import { Badge } from '../../../../shared/presentation/ui/badge';
import { Button } from '../../../../shared/presentation/ui/button';

export function FolderMetric({
  value,
  detail,
}: {
  value: string;
  detail: string;
}) {
  return (
    <Badge
      variant="outline"
      className="h-9 max-w-full gap-1.5 bg-white px-2.5 py-1.5 text-foreground shadow-sm"
      title={`Folder total: ${value} (${detail}, including descendants)`}
    >
      <Folder
        aria-hidden="true"
        className="h-3.5 w-3.5 shrink-0 text-muted-foreground"
      />
      <span className="truncate text-muted-foreground">Folder total</span>
      <span className="shrink-0 font-semibold">{value}</span>
      <span className="shrink-0 font-normal text-muted-foreground">
        {detail}
      </span>
    </Badge>
  );
}

export function TrashEmptyState() {
  return (
    <div className="grid min-h-72 place-items-center p-8 text-center">
      <div className="grid max-w-sm gap-2">
        <Trash2
          aria-hidden="true"
          className="mx-auto h-10 w-10 text-muted-foreground"
        />
        <h2 className="font-semibold">Trash is empty</h2>
        <p className="text-sm text-muted-foreground">
          Items moved to trash stay recoverable until you permanently delete
          them.
        </p>
      </div>
    </div>
  );
}

export function Breadcrumbs({
  entries,
  currentPath,
  onSelectPath,
}: {
  entries: FileEntry[];
  currentPath: string;
  onSelectPath: (path: string) => void;
}) {
  const logicalPath = normalizePath(currentPath);
  const [copiedPath, setCopiedPath] = useState<string>();
  const copyResetTimerRef = useRef<number | undefined>(undefined);
  const currentLogicalPathRef = useRef(logicalPath);
  currentLogicalPathRef.current = logicalPath;
  const crumbs =
    entries.length > 0
      ? entries
      : [
          {
            path: '',
            name: formatPathDisplayName(''),
            kind: 'directory' as const,
            size: 0,
            updatedAt: '',
          },
        ];

  useEffect(() => {
    setCopiedPath(undefined);
    return () => {
      if (copyResetTimerRef.current !== undefined) {
        window.clearTimeout(copyResetTimerRef.current);
      }
    };
  }, [logicalPath]);

  async function handleCopyPath() {
    try {
      await navigator.clipboard.writeText(logicalPath);
      if (currentLogicalPathRef.current !== logicalPath) {
        return;
      }
      if (copyResetTimerRef.current !== undefined) {
        window.clearTimeout(copyResetTimerRef.current);
      }
      setCopiedPath(logicalPath);
      copyResetTimerRef.current = window.setTimeout(() => {
        setCopiedPath((path) => (path === logicalPath ? undefined : path));
        copyResetTimerRef.current = undefined;
      }, 1500);
    } catch {
      // Clipboard access can be unavailable outside a secure browser context.
    }
  }

  const isCopied = copiedPath === logicalPath;

  return (
    <div className="flex min-w-0 items-center gap-2 text-sm">
      <div className="min-w-0 flex-1 overflow-x-auto">
        <div className="flex min-w-max flex-nowrap items-center gap-1">
          {crumbs.map((entry, index) => {
            const path = normalizePath(entry.path);
            const label = formatPathDisplayName(path, entry.name);
            const isLast = path === logicalPath || index === crumbs.length - 1;
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
                  title={path === '' ? label : path}
                >
                  {label}
                </Button>
                {!isLast && <span className="text-muted-foreground">/</span>}
              </div>
            );
          })}
        </div>
      </div>
      <Button
        variant="outline"
        size="icon"
        onClick={() => void handleCopyPath()}
        aria-label={isCopied ? '已複製路徑' : '複製路徑'}
        title={isCopied ? '已複製路徑' : '複製路徑'}
      >
        {isCopied ? (
          <Check aria-hidden="true" className="h-4 w-4" />
        ) : (
          <Copy aria-hidden="true" className="h-4 w-4" />
        )}
      </Button>
    </div>
  );
}
