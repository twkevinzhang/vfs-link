import {
  Archive,
  Eye,
  EyeOff,
  FileArchive,
  FolderOpen,
  ImageIcon,
  LoaderCircle,
  Trash2,
  Upload,
} from 'lucide-react';
import { useCallback, useMemo, useRef, useState } from 'react';

import {
  buildArchives,
  removeArchiveTemporaryFiles,
  type ArchiveBuildProgress,
} from '../lib/archive-compression';
import { buildArchivePlans, type ArchiveOptions } from '../lib/archive-plan';
import { firstDecodableThumbnail } from '../lib/archive-thumbnail';
import {
  collectDroppedFiles,
  filesToUploadCandidates,
  type UploadCandidate,
} from '../lib/folder-upload';
import { formatBytes } from '../lib/format';
import { cn } from '../lib/utils';
import { Button } from './ui/button';
import { Checkbox } from './ui/checkbox';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from './ui/dialog';
import { Input } from './ui/input';

export type PreparedArchiveBatch = {
  id: string;
  candidates: UploadCandidate[];
  thumbnail?: { blob: Blob; width: number; height: number };
  temporaryNames: string[];
};

const DEFAULT_OPTIONS: ArchiveOptions = {
  archiveName: 'upload.zip',
  compressionLevel: 6,
  splitSize: 0,
  password: '',
  oneArchivePerItem: false,
  preserveExtension: false,
  recurseFolders: false,
};

export function UploadDialog({
  currentPath,
  onAddFiles,
  onAddArchives,
  open,
  onOpenChange,
}: {
  currentPath: string;
  onAddFiles: (candidates: UploadCandidate[]) => void;
  onAddArchives: (batches: PreparedArchiveBatch[]) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [mode, setMode] = useState<'select' | 'archive'>('select');
  const [candidates, setCandidates] = useState<UploadCandidate[]>([]);
  const [dragging, setDragging] = useState(false);
  const [selectionError, setSelectionError] = useState<string>();
  const [options, setOptions] = useState(DEFAULT_OPTIONS);
  const [confirmPassword, setConfirmPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [building, setBuilding] = useState(false);
  const [progress, setProgress] = useState<ArchiveBuildProgress>();
  const [thumbnailSelections, setThumbnailSelections] = useState<
    Record<string, string>
  >({});
  const abortRef = useRef<AbortController | undefined>(undefined);
  const inputRef = useRef<HTMLInputElement>(null);
  const folderInputRef = useRef<HTMLInputElement>(null);

  const reset = useCallback(() => {
    setMode('select');
    setCandidates([]);
    setDragging(false);
    setSelectionError(undefined);
    setOptions(DEFAULT_OPTIONS);
    setConfirmPassword('');
    setShowPassword(false);
    setBuilding(false);
    setProgress(undefined);
    setThumbnailSelections({});
  }, []);

  const handleOpenChange = useCallback(
    (nextOpen: boolean) => {
      if (!nextOpen && building) return;
      if (!nextOpen) reset();
      onOpenChange(nextOpen);
    },
    [building, onOpenChange, reset]
  );

  const addCandidates = useCallback((next: UploadCandidate[]) => {
    setSelectionError(undefined);
    if (next.length === 0) {
      setSelectionError('選取的資料夾中沒有檔案。');
      return;
    }
    setCandidates((current) => {
      const seen = new Set(
        current.map(
          (item) =>
            `${item.relativePath}:${item.file.size}:${item.file.lastModified}`
        )
      );
      return [
        ...current,
        ...next.filter(
          (item) =>
            !seen.has(
              `${item.relativePath}:${item.file.size}:${item.file.lastModified}`
            )
        ),
      ];
    });
  }, []);

  const plans = useMemo(
    () => buildArchivePlans(candidates, options),
    [candidates, options]
  );
  const totalBytes = candidates.reduce((sum, item) => sum + item.file.size, 0);
  const passwordsMatch = options.password === confirmPassword;

  const buildAndQueue = useCallback(async () => {
    if (options.password && !passwordsMatch) {
      setSelectionError('兩次輸入的密碼不一致。');
      return;
    }
    const controller = new AbortController();
    abortRef.current = controller;
    setBuilding(true);
    setSelectionError(undefined);
    let built: Awaited<ReturnType<typeof buildArchives>> = [];
    try {
      const preparedThumbnails = await Promise.all(
        plans.map((plan) => {
          const selectedPath = thumbnailSelections[plan.id];
          const ordered = selectedPath
            ? [
                ...plan.thumbnailCandidates.filter(
                  (item) => item.relativePath === selectedPath
                ),
                ...plan.thumbnailCandidates.filter(
                  (item) => item.relativePath !== selectedPath
                ),
              ]
            : plan.thumbnailCandidates;
          return firstDecodableThumbnail(ordered);
        })
      );
      built = await buildArchives(plans, {
        compressionLevel: options.compressionLevel,
        splitSize: options.splitSize,
        password: options.password,
        signal: controller.signal,
        onProgress: setProgress,
      });
      const batches = built.map((result, index) => {
        const groupId = result.id;
        return {
          id: groupId,
          candidates: result.files.map((candidate) => ({
            ...candidate,
            archiveGroupId: groupId,
          })),
          thumbnail: preparedThumbnails[index]?.thumbnail,
          temporaryNames: result.temporaryNames,
        };
      });
      onAddArchives(batches);
      handleOpenChange(false);
    } catch (error) {
      await Promise.all(
        built.map((result) =>
          removeArchiveTemporaryFiles(result.temporaryNames)
        )
      );
      if ((error as { name?: string }).name !== 'AbortError') {
        setSelectionError(
          error instanceof Error ? error.message : '建立壓縮檔失敗。'
        );
      }
    } finally {
      abortRef.current = undefined;
      setBuilding(false);
    }
  }, [
    handleOpenChange,
    onAddArchives,
    options,
    passwordsMatch,
    plans,
    thumbnailSelections,
  ]);

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-4xl">
        <div>
          <DialogTitle className="font-semibold">
            {mode === 'select' ? '上傳檔案' : '加到壓縮檔'}
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            目的地：<span className="font-mono">{currentPath || '/'}</span>
          </DialogDescription>
        </div>

        {mode === 'select' ? (
          <>
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
                  .catch((error: unknown) =>
                    setSelectionError(
                      error instanceof Error
                        ? error.message
                        : '無法讀取拖放的資料夾。'
                    )
                  );
              }}
            >
              <Upload aria-hidden="true" className="h-6 w-6 text-accent" />
              <span className="font-medium">拖放檔案或資料夾，或選擇檔案</span>
              <span className="text-xs text-muted-foreground">
                選取內容會先暫存於此，不會立即上傳。
              </span>
            </button>
            <input
              ref={inputRef}
              type="file"
              multiple
              className="sr-only"
              onChange={(event) => {
                if (event.target.files)
                  addCandidates(filesToUploadCandidates(event.target.files));
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
                if (event.target.files)
                  addCandidates(filesToUploadCandidates(event.target.files));
                event.target.value = '';
              }}
            />
            <div className="flex justify-between gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => folderInputRef.current?.click()}
              >
                <FolderOpen className="h-4 w-4" /> 選擇資料夾
              </Button>
              <span className="text-sm text-muted-foreground">
                {candidates.length} 個檔案 · {formatBytes(totalBytes)}
              </span>
            </div>
            {candidates.length > 0 && (
              <div className="max-h-56 overflow-auto rounded-lg border border-border">
                {candidates.map((candidate, index) => (
                  <div
                    key={`${candidate.relativePath}-${index}`}
                    className="flex items-center gap-3 border-b border-border px-3 py-2 last:border-0"
                  >
                    <FileArchive className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <span className="min-w-0 flex-1 truncate text-sm">
                      {candidate.relativePath}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {formatBytes(candidate.file.size)}
                    </span>
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      className="h-7 w-7"
                      aria-label={`移除 ${candidate.relativePath}`}
                      onClick={() =>
                        setCandidates((current) =>
                          current.filter(
                            (_, candidateIndex) => candidateIndex !== index
                          )
                        )
                      }
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            )}
            <div className="flex justify-end gap-2 border-t border-border pt-4">
              <Button variant="outline" onClick={() => handleOpenChange(false)}>
                取消
              </Button>
              <Button
                variant="outline"
                disabled={candidates.length === 0}
                onClick={() => {
                  onAddFiles(candidates);
                  handleOpenChange(false);
                }}
              >
                直接上傳
              </Button>
              <Button
                disabled={candidates.length === 0}
                onClick={() => setMode('archive')}
              >
                <Archive className="h-4 w-4" /> 加到壓縮檔
              </Button>
            </div>
          </>
        ) : (
          <>
            <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_300px]">
              <div className="grid content-start gap-4">
                <label className="grid gap-1 text-sm font-medium">
                  壓縮檔案名稱
                  <Input
                    value={options.archiveName}
                    disabled={options.oneArchivePerItem || building}
                    onChange={(event) =>
                      setOptions((current) => ({
                        ...current,
                        archiveName: event.target.value,
                      }))
                    }
                  />
                </label>
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-1 text-sm font-medium">
                    壓縮方式
                    <select
                      className="h-10 rounded-md border border-input bg-white px-3 text-sm"
                      value={options.compressionLevel}
                      disabled={building}
                      onChange={(event) =>
                        setOptions((current) => ({
                          ...current,
                          compressionLevel: Number(event.target.value),
                        }))
                      }
                    >
                      <option value={0}>儲存（不壓縮）</option>
                      <option value={3}>快速</option>
                      <option value={6}>一般</option>
                      <option value={9}>最佳</option>
                    </select>
                  </label>
                  <label className="grid gap-1 text-sm font-medium">
                    分割檔大小（MB）
                    <Input
                      type="number"
                      min={0}
                      step={1}
                      value={
                        options.splitSize ? options.splitSize / 1024 / 1024 : 0
                      }
                      disabled={building}
                      onChange={(event) =>
                        setOptions((current) => ({
                          ...current,
                          splitSize:
                            Math.max(0, Number(event.target.value)) *
                            1024 *
                            1024,
                        }))
                      }
                    />
                  </label>
                </div>
                <fieldset className="grid gap-3 rounded-lg border border-border p-4">
                  <legend className="px-1 text-sm font-semibold">
                    壓縮檔分組
                  </legend>
                  <label className="flex items-center gap-2 text-sm">
                    <Checkbox
                      checked={options.oneArchivePerItem}
                      disabled={building}
                      onChange={(event) =>
                        setOptions((current) => ({
                          ...current,
                          oneArchivePerItem: event.target.checked,
                          preserveExtension: event.target.checked
                            ? current.preserveExtension
                            : false,
                          recurseFolders: event.target.checked
                            ? current.recurseFolders
                            : false,
                        }))
                      }
                    />
                    一個項目建立一個壓縮檔
                  </label>
                  <div className="ml-6 grid gap-3 border-l border-border pl-4">
                    <label className="flex items-center gap-2 text-sm">
                      <Checkbox
                        checked={options.preserveExtension}
                        disabled={!options.oneArchivePerItem || building}
                        onChange={(event) =>
                          setOptions((current) => ({
                            ...current,
                            preserveExtension: event.target.checked,
                          }))
                        }
                      />
                      保留原始副檔名（photo.jpg.zip）
                    </label>
                    <label className="flex items-center gap-2 text-sm">
                      <Checkbox
                        checked={options.recurseFolders}
                        disabled={!options.oneArchivePerItem || building}
                        onChange={(event) =>
                          setOptions((current) => ({
                            ...current,
                            recurseFolders: event.target.checked,
                          }))
                        }
                      />
                      資料夾內每個檔案也各自建立壓縮檔
                    </label>
                  </div>
                </fieldset>
                <fieldset className="grid gap-3 rounded-lg border border-border p-4">
                  <legend className="px-1 text-sm font-semibold">
                    設定密碼
                  </legend>
                  <div className="relative">
                    <Input
                      type={showPassword ? 'text' : 'password'}
                      placeholder="密碼（選填）"
                      autoComplete="new-password"
                      value={options.password}
                      disabled={building}
                      onChange={(event) =>
                        setOptions((current) => ({
                          ...current,
                          password: event.target.value,
                        }))
                      }
                    />
                    <button
                      type="button"
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground"
                      onClick={() => setShowPassword((value) => !value)}
                      aria-label={showPassword ? '隱藏密碼' : '顯示密碼'}
                    >
                      {showPassword ? (
                        <EyeOff className="h-4 w-4" />
                      ) : (
                        <Eye className="h-4 w-4" />
                      )}
                    </button>
                  </div>
                  <Input
                    type={showPassword ? 'text' : 'password'}
                    placeholder="再次輸入密碼"
                    autoComplete="new-password"
                    value={confirmPassword}
                    disabled={building}
                    onChange={(event) => setConfirmPassword(event.target.value)}
                  />
                  <p className="text-xs text-muted-foreground">
                    密碼只在此瀏覽器用於 AES-256 加密，不會傳送到伺服器。
                  </p>
                </fieldset>
              </div>

              <aside className="grid max-h-[58dvh] content-start gap-3 overflow-auto rounded-lg bg-muted/35 p-3">
                <div>
                  <h3 className="text-sm font-semibold">輸出與縮圖</h3>
                  <p className="text-xs text-muted-foreground">
                    {plans.length} 個壓縮檔；預設採用字典序第一張可解碼圖片。
                  </p>
                </div>
                {plans.map((plan) => (
                  <div
                    key={plan.id}
                    className="grid gap-2 rounded-md border border-border bg-white p-3"
                  >
                    <div className="flex items-center gap-2 text-sm font-medium">
                      <FileArchive className="h-4 w-4 text-[#276c93]" />
                      <span className="truncate" title={plan.name}>
                        {plan.name}
                      </span>
                    </div>
                    {plan.thumbnailCandidates.length > 0 ? (
                      <label className="grid gap-1 text-xs text-muted-foreground">
                        <span className="flex items-center gap-1">
                          <ImageIcon className="h-3.5 w-3.5" />
                          縮圖來源
                        </span>
                        <select
                          className="h-9 min-w-0 rounded-md border border-input bg-white px-2 text-sm text-foreground"
                          value={
                            thumbnailSelections[plan.id] ??
                            plan.thumbnailCandidates[0].relativePath
                          }
                          disabled={building}
                          onChange={(event) =>
                            setThumbnailSelections((current) => ({
                              ...current,
                              [plan.id]: event.target.value,
                            }))
                          }
                        >
                          {plan.thumbnailCandidates.map((candidate) => (
                            <option
                              key={candidate.relativePath}
                              value={candidate.relativePath}
                            >
                              {candidate.relativePath}
                            </option>
                          ))}
                        </select>
                      </label>
                    ) : (
                      <p className="text-xs text-muted-foreground">
                        沒有可用圖片，不建立縮圖
                      </p>
                    )}
                  </div>
                ))}
              </aside>
            </div>

            {building && progress && (
              <div className="grid gap-2 rounded-lg border border-accent/30 bg-accent/5 p-3 text-sm">
                <div className="flex justify-between gap-3">
                  <span className="truncate">
                    正在建立 {progress.archiveName}
                  </span>
                  <span>
                    {progress.archiveIndex + 1}/{progress.archiveCount}
                  </span>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full bg-accent transition-[width]"
                    style={{
                      width: `${
                        ((progress.entryIndex + progress.progress) /
                          progress.entryCount) *
                        100
                      }%`,
                    }}
                  />
                </div>
              </div>
            )}

            <div className="sticky -bottom-5 z-10 -mx-5 -mb-5 flex justify-end gap-2 border-t border-border bg-white px-5 pb-5 pt-4">
              <Button
                variant="outline"
                disabled={building}
                onClick={() => setMode('select')}
              >
                返回
              </Button>
              {building && (
                <Button
                  variant="destructive"
                  onClick={() => abortRef.current?.abort()}
                >
                  取消壓縮
                </Button>
              )}
              <Button
                disabled={building || !passwordsMatch || plans.length === 0}
                onClick={() => void buildAndQueue()}
              >
                {building ? (
                  <LoaderCircle className="h-4 w-4 animate-spin" />
                ) : (
                  <Archive className="h-4 w-4" />
                )}
                {building ? '建立中…' : '建立並上傳'}
              </Button>
            </div>
          </>
        )}

        {selectionError && (
          <p className="text-sm text-destructive" role="alert">
            {selectionError}
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}
