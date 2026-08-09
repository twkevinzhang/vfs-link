import { FilesPage } from '../presentation/files-page';
import {
  createFilesControllerDependencies,
  filesPresentationDependencies,
  type FilesContextIntegrations,
} from './files-context';

/**
 * Builds the route component at the context boundary. Cross-context behavior
 * is supplied as callbacks by the application composition root.
 */
export function createFilesRoute(integrations: FilesContextIntegrations) {
  const controllerDependencies =
    createFilesControllerDependencies(integrations);

  return function FilesRoute() {
    return (
      <FilesPage
        controllerDependencies={controllerDependencies}
        presentationDependencies={filesPresentationDependencies}
        uploadIntegration={integrations.upload}
      />
    );
  };
}

export { meta } from '../presentation/files-page';
