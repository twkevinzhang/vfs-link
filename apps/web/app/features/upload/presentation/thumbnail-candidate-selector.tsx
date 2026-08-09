import { useDeferredValue, useMemo, useState } from 'react';

import { searchableThumbnailOptions } from './dialog-window';
import type { UploadCandidate } from './upload-candidates';
import { Input } from '../../../shared/presentation/ui/input';

export function ThumbnailCandidateSelector({
  candidates,
  selectedPath,
  disabled,
  onSelect,
}: {
  candidates: UploadCandidate[];
  selectedPath: string;
  disabled: boolean;
  onSelect(path: string): void;
}) {
  const [query, setQuery] = useState('');
  const deferredQuery = useDeferredValue(query);
  const options = useMemo(
    () => searchableThumbnailOptions(candidates, deferredQuery, selectedPath),
    [candidates, deferredQuery, selectedPath]
  );

  return (
    <div className="grid gap-1.5">
      <Input
        type="search"
        value={query}
        disabled={disabled}
        placeholder="搜尋縮圖來源"
        aria-label="搜尋縮圖來源"
        onChange={(event) => setQuery(event.target.value)}
      />
      <select
        className="h-9 min-w-0 rounded-md border border-input bg-white px-2 text-sm text-foreground"
        value={selectedPath}
        disabled={disabled}
        onChange={(event) => onSelect(event.target.value)}
      >
        {options.map((candidate) => (
          <option key={candidate.relativePath} value={candidate.relativePath}>
            {candidate.relativePath}
          </option>
        ))}
      </select>
      <span className="text-[11px] text-muted-foreground">
        顯示 {options.length} / {candidates.length}；輸入路徑可尋找其餘項目
      </span>
    </div>
  );
}
