import {
  Download,
  ExternalLink,
  FolderInput,
  MoreHorizontal,
  Share2,
  Trash2,
} from 'lucide-react';
import { useEffect, useState } from 'react';

import { getDownloadUrl, getTree } from '../lib/api';
import type { FileEntry, TreeNode } from '../types/files';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from './ui/alert-dialog';
import { Button } from './ui/button';
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from './ui/dialog';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from './ui/dropdown-menu';

export function FileActionMenu({
  entry,
  sharing,
  onOpen,
  onShare,
  onMove,
  onTrash,
}: {
  entry: FileEntry;
  sharing?: boolean;
  onOpen: () => void;
  onShare: () => void;
  onMove: () => void;
  onTrash: () => void;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          aria-label={`Actions for ${entry.name}`}
        >
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={onOpen}>
          <ExternalLink className="h-4 w-4" />
          Open
        </DropdownMenuItem>
        {entry.kind === 'file' && (
          <DropdownMenuItem asChild>
            <a href={getDownloadUrl(entry.path)}>
              <Download className="h-4 w-4" />
              Download
            </a>
          </DropdownMenuItem>
        )}
        {entry.kind === 'file' && (
          <DropdownMenuItem disabled={sharing} onSelect={onShare}>
            <Share2 className="h-4 w-4" />
            Share
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator className="my-1 h-px bg-border" />
        <DropdownMenuItem onSelect={onMove}>
          <FolderInput className="h-4 w-4" />
          Move
        </DropdownMenuItem>
        <DropdownMenuItem
          className="text-destructive focus:text-destructive"
          onSelect={onTrash}
        >
          <Trash2 className="h-4 w-4" />
          Move to trash
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function MoveDialog({
  open,
  count,
  initialPath = '/',
  onOpenChange,
  onMove,
}: {
  open: boolean;
  count: number;
  initialPath?: string;
  onOpenChange: (open: boolean) => void;
  onMove: (destination: string) => Promise<void>;
}) {
  const [current, setCurrent] = useState(initialPath);
  const [tree, setTree] = useState<TreeNode>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!open) return;
    setCurrent(initialPath);
  }, [initialPath, open]);

  useEffect(() => {
    if (!open) return;
    let active = true;
    setLoading(true);
    setError(undefined);
    void getTree(current)
      .then((value) => active && setTree(value))
      .catch(
        (reason) =>
          active &&
          setError(
            reason instanceof Error ? reason.message : 'Unable to load folders'
          )
      )
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [current, open]);

  const goUp = () => {
    if (current === '/') return;
    const parts = current.split('/').filter(Boolean);
    parts.pop();
    setCurrent(parts.length ? `/${parts.join('/')}` : '/');
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <div className="grid gap-1 pr-7">
          <DialogTitle className="text-lg font-semibold">
            Move {count} item{count === 1 ? '' : 's'}
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            Choose the destination folder. Existing names will not be
            overwritten.
          </DialogDescription>
        </div>
        <div className="rounded-md border border-border">
          <div className="flex items-center gap-2 border-b border-border p-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={goUp}
              disabled={current === '/'}
            >
              Up
            </Button>
            <span className="min-w-0 truncate text-sm font-medium">
              {current}
            </span>
          </div>
          <div className="max-h-64 min-h-40 overflow-auto p-2">
            {loading && (
              <p className="p-2 text-sm text-muted-foreground">
                Loading folders…
              </p>
            )}
            {error && <p className="p-2 text-sm text-destructive">{error}</p>}
            {!loading && !error && (tree?.children?.length ?? 0) === 0 && (
              <p className="p-2 text-sm text-muted-foreground">
                No subfolders here.
              </p>
            )}
            {tree?.children?.map((folder) => (
              <button
                key={folder.path}
                type="button"
                className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-sm hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                onClick={() => setCurrent(folder.path)}
              >
                <FolderInput className="h-4 w-4 text-primary" />
                {folder.name}
              </button>
            ))}
          </div>
        </div>
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="outline">Cancel</Button>
          </DialogClose>
          <Button
            disabled={loading || !!error}
            onClick={() => void onMove(current)}
          >
            Move here
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

export function ConfirmTrashDialog({
  open,
  count,
  onOpenChange,
  onConfirm,
}: {
  open: boolean;
  count: number;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => Promise<void>;
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogTitle className="text-lg font-semibold">
          Move to trash?
        </AlertDialogTitle>
        <AlertDialogDescription className="text-sm text-muted-foreground">
          {count} selected item{count === 1 ? '' : 's'} will be hidden from the
          file browser. You can restore them from Trash.
        </AlertDialogDescription>
        <div className="flex justify-end gap-2">
          <AlertDialogCancel asChild>
            <Button variant="outline">Cancel</Button>
          </AlertDialogCancel>
          <AlertDialogAction asChild>
            <Button variant="destructive" onClick={() => void onConfirm()}>
              Move to trash
            </Button>
          </AlertDialogAction>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export function ConfirmPermanentDelete({
  open,
  title,
  description,
  action = 'Delete permanently',
  onOpenChange,
  onConfirm,
}: {
  open: boolean;
  title: string;
  description: string;
  action?: string;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => Promise<void>;
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogTitle className="text-lg font-semibold">
          {title}
        </AlertDialogTitle>
        <AlertDialogDescription className="text-sm text-muted-foreground">
          {description}
        </AlertDialogDescription>
        <div className="flex justify-end gap-2">
          <AlertDialogCancel asChild>
            <Button variant="outline">Cancel</Button>
          </AlertDialogCancel>
          <AlertDialogAction asChild>
            <Button variant="destructive" onClick={() => void onConfirm()}>
              {action}
            </Button>
          </AlertDialogAction>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  );
}
