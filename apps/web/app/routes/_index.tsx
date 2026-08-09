import { Navigate } from 'react-router';

import { FILES_ROUTE } from '../features/files/composition';

export default function IndexRedirect() {
  return <Navigate to={FILES_ROUTE} replace />;
}
