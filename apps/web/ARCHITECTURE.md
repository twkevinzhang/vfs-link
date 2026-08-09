# Frontend domain architecture

The browser application is organized as four bounded contexts. Each context
owns its language, rules, use cases, ports, adapters, views, and composition.

| Context | Ubiquitous language                                                 | Owned behavior                                                              |
| ------- | ------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| Files   | file entry, logical path, tree, trash entry, file operation         | browse, inspect, rename, move, trash, restore, watch operations             |
| Upload  | upload source, queue item, preflight, target decision, chunk, retry | select sources, resolve conflicts, schedule, transfer, pause, retry, cancel |
| Drift   | drift item, scan, plan, action, reconciliation                      | discover drift, price a plan, execute and monitor reconciliation            |
| Share   | share draft, start eligibility, terminal status                     | create, start, poll, and present a share                                    |

## Layers

Every context has the same dependency direction:

```text
route/root
    |
    v
composition -----> infrastructure
    |                    |
    v                    | implements
presentation -----> application ports/use cases
                           |
                           v
                         domain
```

- `domain` contains business types, value policies, state transitions, and
  invariants. It depends only on its context domain and `shared/kernel`.
- `application` contains framework-free use cases, controllers, schedulers,
  cancellation contracts, and consumer-owned ports. It depends only on domain
  and `shared/kernel`.
- `infrastructure` implements application ports for HTTP, browser scheduling,
  binary sources, thumbnails, and cancellation.
- `presentation` contains React pages, hooks, and components. It consumes
  application/domain contracts and shared presentation primitives. It never
  constructs concrete infrastructure.
- `composition` is the only context layer that wires infrastructure to
  application and presentation.

`app/routes` and `app/root.tsx` are application composition roots. Routes only
import context composition entries. Shared code is split into pure kernel,
technical infrastructure, and presentation primitives.

## Context map

Context integration is explicit at the route composition root:

```text
Share composition -- createShareDraft callback --> Files composition
Upload composition -- upload UI/queue contract --> Files composition
Upload composition -- session provider --------> application root
Drift composition -----------------------------> Drift route
Share composition -----------------------------> Share route
```

No context imports another context's domain, application, infrastructure, or
presentation modules. Files owns the consumer-side contracts for Share and
Upload; the route supplies structurally compatible providers.

## Upload lifecycle

The upload queue is session-scoped and memory-only:

- changing routes in the same tab keeps the queue alive through the root
  provider;
- reloading or closing the tab discards unfinished work;
- tabs are independent;
- file and folder selections are ordinary `File` objects held by the Upload
  infrastructure source registry;
- archive output is memory-backed and released on completion, failure, or
  cancellation;
- pause, retry, cancel, preflight decisions, chunk offset reconciliation, and
  automatic retry remain application behavior.

The Upload domain and application layers use source identifiers and binary
ports. Browser objects never cross into them.

## Enforcement

`pnpm boundaries` applies an allowlist to every layer and blocks cycles,
unresolved imports, context crossings, reverse dependencies, presentation to
infrastructure dependencies, and route access to context internals.

`app/architecture-boundaries.test.ts` verifies:

- forbidden layer edges make the real dependency-cruiser command fail;
- domain and application contain no framework, DOM, network, or browser
  storage APIs;
- durable browser queue APIs are absent;
- obsolete top-level business directories and facades are absent.

`pnpm check:web` runs type checking, lint, dependency boundaries, tests, and a
production build. All gates are mandatory.
