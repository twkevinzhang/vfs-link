import { beforeEach, describe, expect, it, vi } from 'vitest';

const routerMocks = vi.hoisted(() => ({
  effect: vi.fn(),
  navigate: vi.fn(),
}));

vi.mock('react', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react')>()),
  useEffect: routerMocks.effect,
}));
vi.mock('react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router')>()),
  useNavigate: () => routerMocks.navigate,
}));
vi.mock('./features/files/composition', () => ({ FILES_ROUTE: '/files' }));

import { IndexRedirect } from './routes/_index';

describe('IndexRedirect', () => {
  beforeEach(() => {
    routerMocks.effect.mockReset();
    routerMocks.navigate.mockReset();
  });

  it('defers the files redirect until the client effect runs', () => {
    expect(IndexRedirect()).toBeNull();
    expect(routerMocks.navigate).not.toHaveBeenCalled();
    expect(routerMocks.effect).toHaveBeenCalledOnce();

    const redirect = routerMocks.effect.mock.calls[0]?.[0];
    expect(redirect).toBeTypeOf('function');
    redirect?.();

    expect(routerMocks.navigate).toHaveBeenCalledWith('/files', {
      replace: true,
    });
  });
});
