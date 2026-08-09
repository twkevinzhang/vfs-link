# Frontend architecture

The browser application is being migrated incrementally around four bounded
contexts:

- **Files** — browsing, selection, move, rename, trash, restore, thumbnails,
  and file inspection.
- **Upload** — source selection, preflight, queue state, retry, chunk transfer,
  archive creation, and persistence.
- **Drift** — scan, plan, action, cost, and reconciliation status.
- **Share** — draft creation, start eligibility, polling, and terminal status.

The enforced dependency direction is:

```text
                           ┌──────────────────────────────┐
                           │ presentation (React adapter) │
                           └──────────────┬───────────────┘
                                          │
                                          v
┌────────────────┐ implements ┌───────────────────────────┐
│ infrastructure │───────────>│ application ports/usecase │
└───────┬────────┘            └──────────────┬────────────┘
        │                                    │
        └────────────────┐                   v
                         │          ┌──────────────────┐
                         └─────────>│ domain rules/types│
                                    └──────────────────┘
```

Application and domain never point back to presentation. Application owns its
ports; infrastructure implements them. Domain modules are framework-free and
may only depend on their domain peers or `shared/kernel`.

React adapters do not construct or import infrastructure. Route modules and
`root.tsx` are the composition roots: they import concrete adapters, assemble
application-owned dependency contracts, and inject those contracts into the
presentation hooks, providers, and components.

```text
route/root composition root
       │
       ├── concrete infrastructure adapters
       │
       └── presentation hook/provider/component ──> application-owned dependency contract
```

## Current context seams

| Context | Domain                                                   | Application                                                             | Infrastructure                               | Presentation                                |
| ------- | -------------------------------------------------------- | ----------------------------------------------------------------------- | -------------------------------------------- | ------------------------------------------- |
| Files   | file/status/operation models                             | `FilesGateway`, controller/presentation dependencies, operation watcher | Files HTTP gateway and URL/thumbnail mapping | injected controller/components              |
| Upload  | queue projection, states, retry policy, archive manifest | upload contracts, `UploadGateway`, queue dependencies                   | upload HTTP/XHR, error and OPFS adapters     | injected queue/dialog seams and list window |
| Drift   | scan/plan/action models and action policies              | `DriftGateway`, controller dependencies, stale-list guard               | Drift HTTP gateway and DTO normalization     | injected controller and formatters          |
| Share   | share model, terminal/start policies                     | `ShareGateway`, abort/generation-aware load/start coordinator           | Share HTTP gateway                           | route composition and view model            |

Cross-cutting HTTP mechanics live in
`app/shared/infrastructure/http/http-client.ts`; business DTO mapping stays in
the owning context gateway. Pagination is a stable shared-kernel value.

`app/lib/api.ts` is now a compatibility facade for existing API contract tests.
Production code is blocked from importing it and imports the owning context
gateway directly. Legacy `app/types/*` files similarly re-export the new source
of truth for compatibility.

## Enforced rules

`pnpm boundaries` is blocking and currently enforces:

- no production dependency cycles;
- no unresolved local imports;
- domain cannot import application, infrastructure, presentation, React
  adapters, routes, or shared infrastructure;
- application cannot import infrastructure or presentation;
- infrastructure cannot import presentation;
- hooks, components, and feature presentation cannot import context
  infrastructure or the archive temporary-storage adapter; dependencies are
  injected by a route/root composition root;
- the four context internals cannot depend on one another;
- production modules cannot use the `lib/api.ts` compatibility facade.

`app/architecture-boundaries.test.ts` creates and removes temporary deliberate
domain-to-presentation plus feature-presentation, legacy-hook, and component
imports of infrastructure. It proves that the real dependency-cruiser
configuration rejects all four. Root `check:web` runs boundaries before
tests/build.

## Transitional debt (not yet claimed as migrated)

- Large React orchestration hooks remain in `app/hooks`. Feature presentation
  seams expose them so their internals can be extracted one workflow at a time
  without changing route contracts.
- Upload runtime queue items still contain `File`, `FileSystemFileHandle`,
  upload session DTOs, and IndexedDB projections. The new domain projection is
  browser-neutral, but the runtime store/persistence split is a later migration.
- Browser archive/OPFS helpers and several upload queue helpers remain under
  `app/lib`, but the queue receives archive storage through an
  application-owned port wired in `root.tsx`. ZIP loading remains
  activation-only and must not be pulled into a synchronous feature barrel.
- UI components still live under `app/components`. They are presentation code;
  moving them under each context is optional and must not precede extracting
  the large controllers/use cases.

These items are explicitly allowed strangler seams. Domain code remains free of
React, fetch, XHR, IndexedDB, and browser file handles. The Upload application
edge temporarily owns the browser-source contract (`File`, `Blob`, and
`FileSystemFileHandle`); moving that contract behind an application-owned port
is tracked transitional debt rather than a completed boundary.
