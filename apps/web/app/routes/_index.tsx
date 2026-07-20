import { Navigate } from 'react-router';

import { FILES_ROUTE } from '../lib/file-route';

export default function IndexRedirect() {
  return <Navigate to={FILES_ROUTE} replace />;
}
