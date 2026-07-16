import { AlertCircle, Check, RotateCcw, Upload, X } from 'lucide-react';
import { useCallback, useRef, useState } from 'react';

import {
  cancelUpload,
  completeUpload,
  createUpload,
  putUpload,
} from '../lib/api';
import { formatBytes, normalizePath } from '../lib/format';
import { cn } from '../lib/utils';
import { Button } from './ui/button';

type UploadState = 'queued' | 'uploading' | 'complete' | 'failed';

type UploadItem = {
  key: string;
  file: File;
  progress: number;
  state: UploadState;
  error?: string;
  sessionId?: string;
};

export function UploadPanel({
  currentPath,
  existingNames,
  onComplete,
  onClose,
}: {
  currentPath: string;
  existingNames: Set<string>;
  onComplete: () => void;
  onClose: () => void;
}) {
  const [items, setItems] = useState<UploadItem[]>([]);
  const [dragging, setDragging] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  const update = useCallback((key: string, patch: Partial<UploadItem>) => {
    setItems((current) =>
      current.map((item) => (item.key === key ? { ...item, ...patch } : item))
    );
  }, []);

  const run = useCallback(
    async (item: UploadItem) => {
      let overwrite = existingNames.has(item.file.name);
      if (
        overwrite &&
        !window.confirm(`${item.file.name} already exists. Replace it?`)
      ) {
        update(item.key, {
          state: 'failed',
          error: 'Existing file was not replaced',
        });
        return;
      }
      update(item.key, { state: 'uploading', progress: 0, error: undefined });
      let sessionId: string | undefined;
      try {
        const logicPath = normalizePath(
          `${currentPath === '/' ? '' : currentPath}/${item.file.name}`
        );
        const createInput = {
          path: logicPath,
          size: item.file.size,
          contentType: item.file.type || 'application/octet-stream',
          overwrite,
        };
        let session;
        try {
          session = await createUpload(createInput);
        } catch (error) {
          const message = error instanceof Error ? error.message : '';
          if (
            !overwrite &&
            message.toLowerCase().includes('already exists') &&
            window.confirm(`${item.file.name} already exists. Replace it?`)
          ) {
            overwrite = true;
            session = await createUpload({ ...createInput, overwrite: true });
          } else {
            throw error;
          }
        }
        sessionId = session.id;
        update(item.key, { sessionId });
        await putUpload(session, item.file, (uploaded, total) => {
          update(item.key, {
            progress: total > 0 ? Math.min(100, (uploaded / total) * 100) : 0,
          });
        });
        await completeUpload(session);
        update(item.key, { state: 'complete', progress: 100 });
        onComplete();
      } catch (error) {
        update(item.key, {
          state: 'failed',
          error: error instanceof Error ? error.message : 'Upload failed',
        });
        if (sessionId) {
          void cancelUpload(sessionId);
        }
      }
    },
    [currentPath, existingNames, onComplete, update]
  );

  const addFiles = useCallback(
    (files: FileList | File[]) => {
      const added = Array.from(files).map((file) => ({
        key: `${file.name}-${file.size}-${
          file.lastModified
        }-${crypto.randomUUID()}`,
        file,
        progress: 0,
        state: 'queued' as const,
      }));
      setItems((current) => [...current, ...added]);
      for (const item of added) {
        void run(item);
      }
    },
    [run]
  );

  return (
    <section
      className="rounded-lg border border-border bg-white p-4 shadow-sm"
      aria-label="Upload files"
    >
      <div className="mb-3 flex items-start justify-between gap-3">
        <div>
          <h2 className="font-semibold">Upload files</h2>
          <p className="text-sm text-muted-foreground">
            Destination: <span className="font-mono">{currentPath}</span>
          </p>
        </div>
        <Button
          variant="ghost"
          size="icon"
          onClick={onClose}
          aria-label="Close upload panel"
        >
          <X aria-hidden="true" className="h-4 w-4" />
        </Button>
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
          addFiles(event.dataTransfer.files);
        }}
      >
        <Upload aria-hidden="true" className="h-6 w-6 text-accent" />
        <span className="font-medium">Drop files here or choose files</span>
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
          if (event.target.files) addFiles(event.target.files);
          event.target.value = '';
        }}
      />

      {items.length > 0 && (
        <ul
          className="mt-3 grid max-h-56 gap-2 overflow-y-auto"
          aria-live="polite"
        >
          {items.map((item) => (
            <li
              key={item.key}
              className="grid gap-1 rounded-md border border-border p-3"
            >
              <div className="flex items-center gap-2 text-sm">
                {item.state === 'complete' ? (
                  <Check
                    className="h-4 w-4 shrink-0 text-[#11615a]"
                    aria-hidden="true"
                  />
                ) : item.state === 'failed' ? (
                  <AlertCircle
                    className="h-4 w-4 shrink-0 text-destructive"
                    aria-hidden="true"
                  />
                ) : (
                  <Upload
                    className="h-4 w-4 shrink-0 text-accent"
                    aria-hidden="true"
                  />
                )}
                <span
                  className="min-w-0 flex-1 truncate font-medium"
                  title={item.file.name}
                >
                  {item.file.name}
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">
                  {formatBytes(item.file.size)}
                </span>
                {item.state === 'failed' && (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void run(item)}
                  >
                    <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />{' '}
                    Retry
                  </Button>
                )}
              </div>
              <div
                className="h-1.5 overflow-hidden rounded-full bg-muted"
                aria-label={`${Math.round(item.progress)}% uploaded`}
              >
                <div
                  className="h-full bg-accent transition-[width]"
                  style={{ width: `${item.progress}%` }}
                />
              </div>
              {item.error && (
                <p className="text-xs text-destructive">{item.error}</p>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
