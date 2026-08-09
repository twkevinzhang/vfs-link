import { FilesController } from '../application/files-controller';
import { appPath } from '../../../shared/infrastructure/http/base-path';
import {
  filesHttpGateway,
  getDownloadUrl,
  getPreviewUrl,
  getThumbnailUrl,
} from '../infrastructure/files-http-gateway';
import { browserFilesScheduler } from '../infrastructure/browser-files-scheduler';
import type {
  FilesControllerDependencies,
  FilesUploadIntegration,
} from '../presentation/files-presentation-contracts';

export type FilesContextIntegrations = Pick<
  FilesControllerDependencies,
  'createShareDraft'
> & { upload: FilesUploadIntegration };

export const filesController = new FilesController({
  port: filesHttpGateway,
  scheduler: browserFilesScheduler,
});

export const filesPresentationDependencies = {
  loadTree: filesHttpGateway.getTree,
  getDownloadUrl,
  getPreviewUrl,
  getThumbnailUrl,
};

export function createFilesControllerDependencies(
  integrations: FilesContextIntegrations
): FilesControllerDependencies {
  return {
    controller: filesController,
    getPreviewUrl,
    resolveAppPath: appPath,
    createShareDraft: integrations.createShareDraft,
  };
}
