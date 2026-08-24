# LeapView UI North Star

LeapView UI is a stream-first MPA. Go renders documents with Gomponents, Datastar stores client signals, `/updates` streams signal patches, and Lit renders route roots from the Datastar signal store.

It is not a React-style SPA.

## Contract

```text
HTML = structure, assets, primitive route config, security metadata
/updates = all server/read-model state
Datastar = client signal store and signal-patch transport
Lit = render from Datastar through DatastarLit
commands = CQRS write endpoints that publish signal patches
```

Server routes are real URLs and full document navigations. Route identity belongs in the URL, not in a client router.

## Runtime

```text
server route
  -> structural HTML shell
  -> literal data-init="@get('/updates?...', {openWhenHidden: true})"
  -> server-mounted route root
  -> /updates sends hydration signal patch
  -> Datastar stores signals
  -> DatastarLit schedules Lit renders
  -> Lit emits domain events
  -> Datastar posts command signals to CQRS endpoints
  -> commands publish signal patches to the existing stream
```

Initial HTML must not serialize the read model. Route roots render stable loading or empty states until `/updates` hydrates chrome, page view models, domain data, and status signals.

## Boundaries

- Product Pagestream (`pkg/pagestream`): document shell, caller-supplied Datastar asset and same-origin update URL, signal-only SSE streams, and bounded brokers that disconnect slow subscribers.
- Product web transport: anonymous client identity and cookie policy. Dashboard stream transport: generation ordering and explicit result coalescing.
- Go route handlers: auth, permissions, routing, route metadata, command endpoints, read-model patch generation.
- `/updates`: canonical transport endpoint; dispatches by route metadata; sends first hydration patch and later broker patches.
- Datastar: page-local signal graph, SSE patch application, declarative event-to-command wiring.
- `DatastarLit`: only route-root bridge from Datastar signals to Lit.
- Lit: layout, visual composition, local UI state, child component orchestration, component events.
- Gomponents: document shell, assets, route-root mounting, primitive config, CSRF metadata.

Gomponents must not build product UI internals. Lit must not become a router, data-fetching layer, backend-state owner, or Datastar write API.

## Document Shape

Go chooses the route root. `lv-app-shell` provides chrome and a page slot; it does not switch on route kind.

```html
<main data-init="@get('/updates?...', {openWhenHidden: true})">
  <lv-app-shell>
    <lv-some-route-root slot="page" route-id="..."></lv-some-route-root>
  </lv-app-shell>
</main>
```

Allowed route-root host attributes: primitive static config such as ids, active section, view, labels, and booleans.

Forbidden for MPA route payloads: `data-signals`, large JSON attributes, `data-attr:*` mirrors, and duplicated server/read-model state. `data-signals` is only acceptable in isolated component tests or local Datastar controls.

Route roots read Datastar signals through `DatastarLit`. Child components receive normal Lit properties from route roots. Domain dispatch stays inside the owning route root.

## Streams

`/updates` URLs must contain enough route metadata to reconstruct hydration state without pre-existing Datastar signals. The exact query shape belongs to route code and tests, not this document.

`/updates` sends Datastar signal patches only: no element morphs, script events, or mixed transports.

First patch: hydrate route state. Later patches: command results, broker events, refresh/status changes, and domain data that becomes ready after the stream opens.

## Commands

Commands are CQRS write endpoints. They receive command/read context from Datastar, perform domain work, and publish signal patches to the existing `/updates` stream.

Commands must not reopen `/updates`, return ad hoc JSON read models, or make Lit fetch backend state directly.

CSRF is document security metadata, not application state. Mutating pages render `<meta name="csrf-token" content="...">`; Datastar command expressions call `window.LeapViewCommand.headers()`.

## Signals

Signals are product API contracts.

- Go structs are the source of truth.
- JSON Schema and TypeScript types are generated from Go signal structs.
- Lit imports generated types for route, chrome, status, and domain signals.
- Contract tests enforce signal references and prevent unused payloads unless explicitly marked preloaded.
- Signal roots should be stable, route-owned, and shaped for rendering rather than backend convenience.

Dashboard signal roots should remain explicit and renderer-neutral:

```text
$chrome
$page
$filterContract
$filterState
$filterOptionPages
$interactionSelections
$spatialSelections
$visuals
$status
```

## Components

Route roots map one server route to one Lit composition boundary. They read Datastar signals, derive child props, and emit domain events.

Child components are reusable UI units. They do not know about `/updates`, route metadata, auth, permissions, or command endpoint details unless they are the route root for that domain.

Shared components must be static or property-driven. They must not introduce hidden global stores or backend fetch paths.

## Styling

- Tailwind is for outer light DOM and minimal Go shell.
- Lit components use Shadow DOM CSS.
- Lit CSS consumes Primer and LeapView CSS variables.
- Tailwind utilities are not injected into Shadow DOM.
- Global CSS contains tokens, imports, source declarations, and document defaults.
- Product UI must not depend on global selectors.

## Folders

`web/components/` is organized by domain plus `shared/` and `inspector/`. Route bundles should be coarse enough for lazy loading without duplicating shared dependencies. The server includes only common shell assets and the current route root bundle.
