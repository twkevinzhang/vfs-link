import { afterEach, expect, it, vi } from 'vitest';

import { firstDecodableThumbnail } from './archive-thumbnail';
import type { UploadCandidate } from './upload-candidates';

afterEach(() => vi.unstubAllGlobals());

it('stops thumbnail candidate traversal as soon as the signal aborts', async () => {
  const controller = new AbortController();
  const decode = vi.fn(async () => {
    controller.abort();
    throw new Error('corrupt image');
  });
  vi.stubGlobal('createImageBitmap', decode);
  const candidates: UploadCandidate[] = Array.from(
    { length: 10_000 },
    (_, index) => ({
      file: new File(['x'], `image-${index}.jpg`, { type: 'image/jpeg' }),
      relativePath: `image-${index}.jpg`,
      selectionRoot: `image-${index}.jpg`,
      selectionRootKind: 'file',
    })
  );

  await expect(
    firstDecodableThumbnail(candidates, controller.signal)
  ).rejects.toMatchObject({ name: 'AbortError' });
  expect(decode).toHaveBeenCalledOnce();
});
