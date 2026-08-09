import { describe, expect, it } from 'vitest';

import { validateFileName } from './file-name';

describe('validateFileName', () => {
  it('trims leading and trailing whitespace from a valid name', () => {
    expect(validateFileName('  photo final.png  ')).toEqual({
      name: 'photo final.png',
    });
  });

  it.each([
    ['   ', 'Name cannot be empty.'],
    ['.', 'Name cannot be "." or "..".'],
    ['..', 'Name cannot be "." or "..".'],
    ['folder/name', 'Name cannot include /.'],
  ])('rejects %s', (value, error) => {
    expect(validateFileName(value)).toEqual({
      name: value.trim(),
      error,
    });
  });
});
