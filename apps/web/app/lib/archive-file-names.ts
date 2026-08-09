function cleanArchiveName(value: string) {
  const name = value.trim().replace(/[\\/]/g, '-');
  return /\.zip$/i.test(name) ? name : `${name || 'upload'}.zip`;
}

export function splitArchiveNames(name: string, partCount: number) {
  if (partCount <= 1) return [cleanArchiveName(name)];
  const stem = cleanArchiveName(name).slice(0, -4);
  return Array.from({ length: partCount }, (_, index) =>
    index === partCount - 1
      ? `${stem}.zip`
      : `${stem}.z${String(index + 1).padStart(2, '0')}`
  );
}
