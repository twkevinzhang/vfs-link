import type { LinksFunction } from 'react-router';
import { Links, Meta, Outlet, Scripts, ScrollRestoration } from 'react-router';

import './app.css';
import { UploadProvider } from './features/upload/composition';
import { appPath } from './shared/infrastructure/http/base-path';

export const links: LinksFunction = () => [
  { rel: 'icon', type: 'image/svg+xml', href: appPath('/favicon.svg') },
];

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-Hant">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <Meta />
        <Links />
      </head>
      <body>
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export default function App() {
  return (
    <UploadProvider>
      <Outlet />
    </UploadProvider>
  );
}
