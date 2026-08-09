import { useParams } from 'react-router';

import { browserShareScheduler } from '../infrastructure/browser-share-scheduler';
import { shareHttpGateway } from '../infrastructure/share-http-gateway';
import { SharePage } from '../presentation/share-page';
import { useShareController as useShareControllerWith } from '../presentation/use-share-controller';

function useShareController(shareId: string | undefined) {
  return useShareControllerWith(
    shareId,
    shareHttpGateway,
    browserShareScheduler
  );
}

export default function SharePageComposition() {
  const { shareId } = useParams();
  const controller = useShareController(shareId);
  return <SharePage controller={controller} />;
}
