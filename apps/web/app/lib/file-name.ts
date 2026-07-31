export type FileNameValidationResult =
  | { name: string; error?: never }
  | { name: string; error: string };

export function validateFileName(value: string): FileNameValidationResult {
  const name = value.trim();

  if (!name) {
    return { name, error: 'Name cannot be empty.' };
  }
  if (name === '.' || name === '..') {
    return { name, error: 'Name cannot be "." or "..".' };
  }
  if (name.includes('/')) {
    return { name, error: 'Name cannot include /.' };
  }

  return { name };
}
