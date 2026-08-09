import { browserDriftScheduler } from '../infrastructure/browser-drift-scheduler';
import { driftHttpGateway } from '../infrastructure/drift-http-gateway';
import { DriftPage } from '../presentation/drift-page';
import { useDriftController as useDriftControllerWith } from '../presentation/use-drift-controller';

function useDriftController() {
  return useDriftControllerWith(driftHttpGateway, browserDriftScheduler);
}

export default function DriftPageComposition() {
  const controller = useDriftController();
  return <DriftPage controller={controller} />;
}
