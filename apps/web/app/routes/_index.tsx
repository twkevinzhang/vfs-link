import { useEffect } from 'react';
import { useNavigate } from 'react-router';

import { FILES_ROUTE } from '../features/files/composition';

export function IndexRedirect() {
  const navigate = useNavigate();

  useEffect(() => {
    navigate(FILES_ROUTE, { replace: true });
  }, [navigate]);

  return null;
}

export default IndexRedirect;
