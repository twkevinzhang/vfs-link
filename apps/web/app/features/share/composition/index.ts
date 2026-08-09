import { shareHttpGateway } from '../infrastructure/share-http-gateway';

export { default } from './share-page-composition';

export function createShareDraft(path: string) {
  return shareHttpGateway.createShareDraft(path);
}
