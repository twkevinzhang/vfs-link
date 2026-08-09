import { createFilesRoute, meta } from '../features/files/composition';
import { createShareDraft } from '../features/share/composition';
import { filesUploadIntegration } from '../features/upload/composition';

export { meta };
const FilesRoute = createFilesRoute({
  createShareDraft,
  upload: filesUploadIntegration,
});

export default FilesRoute;
