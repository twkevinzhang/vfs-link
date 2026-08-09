import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it, vi } from 'vitest';

import { ThumbnailCandidateSelector } from './thumbnail-candidate-selector';
import type { UploadCandidate } from './upload-candidates';

it('renders at most 100 option nodes for 10,000 thumbnail candidates', () => {
  const candidates: UploadCandidate[] = Array.from(
    { length: 10_000 },
    (_, index) => ({
      file: new File(['x'], `image-${index}.jpg`, { type: 'image/jpeg' }),
      relativePath: `image-${index}.jpg`,
      selectionRoot: `image-${index}.jpg`,
      selectionRootKind: 'file',
    })
  );
  const markup = renderToStaticMarkup(
    <ThumbnailCandidateSelector
      candidates={candidates}
      selectedPath="image-0.jpg"
      disabled={false}
      onSelect={vi.fn()}
    />
  );

  expect(markup.match(/<option\b/g)).toHaveLength(100);
  expect(markup).toContain('搜尋縮圖來源');
});
