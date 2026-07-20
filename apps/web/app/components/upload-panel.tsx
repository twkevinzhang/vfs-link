import { Upload } from 'lucide-react';
import { useCallback, useRef, useState } from 'react';

import {
  collectDroppedFiles,
  filesToUploadCandidates,
  type UploadCandidate,
} from '../lib/folder-upload';
import { cn } from '../lib/utils';
import { Button } from './ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from './ui/dialog';

export function UploadDialog({
  currentPath,
  onAddFiles,
  open,
  onOpenChange,
}: {
  currentPath: string;
  onAddFiles: (candidates: UploadCandidate[]) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [dragging, setDragging] = useState(false);
  const [selectionError, setSelectionError] = useState<string>();
  const inputRef = useRef<HTMLInputElement>(null);
  const folderInputRef = useRef<HTMLInputElement>(null);

  const handleOpenChange = useCallback(
    (nextOpen: boolean) => {
      if (!nextOpen) {
        setDragging(false);
        setSelectionError(undefined);
      }
      onOpenChange(nextOpen);
    },
    [onOpenChange]
  );

  const addCandidates = useCallback(
    (candidates: UploadCandidate[]) => {
      setSelectionError(undefined);
      if (candidates.length === 0) {
        setSelectionError('The selected folder contains no files.');
        return;
      }
      onAddFiles(candidates);
      handleOpenChange(false);
    },
    [handleOpenChange, onAddFiles]
  );

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-2xl">
        <div className="mb-3 flex items-start justify-between gap-3">
          <div>
            <DialogTitle className="font-semibold">Upload files</DialogTitle>
            <DialogDescription className="text-sm text-muted-foreground">
              Destination: <span className="font-mono">{currentPath}</span>
            </DialogDescription>
          </div>
        </div>

        <button
          type="button"
          className={cn(
            'grid w-full place-items-center gap-2 rounded-lg border border-dashed border-border bg-muted/25 px-4 py-6 text-center transition-colors',
            dragging && 'border-accent bg-accent/10'
          )}
          onClick={() => inputRef.current?.click()}
          onDragEnter={(event) => {
            event.preventDefault();
            setDragging(true);
          }}
          onDragOver={(event) => event.preventDefault()}
          onDragLeave={() => setDragging(false)}
          onDrop={(event) => {
            event.preventDefault();
            setDragging(false);
            void collectDroppedFiles(event.dataTransfer)
              .then(addCandidates)
              .catch((error: unknown) => {
                setSelectionError(
                  error instanceof Error
                    ? error.message
                    : 'Unable to read the dropped folder.'
                );
              });
          }}
        >
          <Upload aria-hidden="true" className="h-6 w-6 text-accent" />
          <span className="font-medium">
            Drop files or folders here, or choose files
          </span>
          <span className="text-xs text-muted-foreground">
            Files upload directly without being loaded into browser memory.
          </span>
        </button>
        <input
          ref={inputRef}
          type="file"
          multiple
          className="sr-only"
          onChange={(event) => {
            if (event.target.files) {
              addCandidates(filesToUploadCandidates(event.target.files));
            }
            event.target.value = '';
          }}
        />
        <input
          ref={(node) => {
            folderInputRef.current = node;
            node?.setAttribute('webkitdirectory', '');
          }}
          type="file"
          multiple
          className="sr-only"
          onChange={(event) => {
            if (event.target.files) {
              addCandidates(filesToUploadCandidates(event.target.files));
            }
            event.target.value = '';
          }}
        />
        <div className="mt-2 flex justify-end">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => folderInputRef.current?.click()}
          >
            Choose folder
          </Button>
        </div>

        {selectionError && (
          <p className="mt-2 text-sm text-destructive" role="alert">
            {selectionError}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}
