export type UploadSourceInspection =
  | { status: 'available'; file: File }
  | { status: 'permission-required' }
  | { status: 'missing' };

export type PermissionAwareFileHandle = FileSystemFileHandle & {
  queryPermission?: (descriptor: { mode: 'read' }) => Promise<PermissionState>;
  requestPermission?: (descriptor: {
    mode: 'read';
  }) => Promise<PermissionState>;
};

export async function inspectUploadSource(
  handle: FileSystemFileHandle
): Promise<UploadSourceInspection> {
  const permissionHandle = handle as PermissionAwareFileHandle;
  if (permissionHandle.queryPermission) {
    try {
      const permission = await permissionHandle.queryPermission({
        mode: 'read',
      });
      if (permission === 'denied') return { status: 'missing' };
      if (permission !== 'granted') return { status: 'permission-required' };
    } catch (error) {
      if ((error as { name?: string }).name === 'NotAllowedError') {
        return { status: 'permission-required' };
      }
      return { status: 'missing' };
    }
  }
  try {
    return { status: 'available', file: await handle.getFile() };
  } catch (error) {
    return (error as { name?: string }).name === 'NotAllowedError'
      ? { status: 'permission-required' }
      : { status: 'missing' };
  }
}
