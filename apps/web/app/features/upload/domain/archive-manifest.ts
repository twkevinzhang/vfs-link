export type ArchiveTemporaryManifest = {
  version: 1;
  ownerId: string;
  createdAt: number;
  files: Array<{ name: string; size: number }>;
};
