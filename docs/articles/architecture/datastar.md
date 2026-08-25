# Datastar signal flow

LeapView uses Datastar as the transport between server-owned application state and Lit browser components. Signals are typed product contracts, not an unstructured global store.

## Bootstrap state

Go renders route HTML with gomponents. The page includes application chrome, custom-element hosts, and the initial signal roots required for that route. UI signal models are generated from the TypeSpec signal contract and normalized by shared helpers so Go and TypeScript agree on optional values and collection shapes.

A component binds to a specific signal path through its host attributes or route configuration. It should not search the entire signal tree for data it happens to recognize.

## Lit bridge

The shared `DatastarLit` bridge tracks signal reads during rendering and schedules a Lit update when a relevant signal changes. It handles cold hydration where roots appear after the element connects and disposes effects when the component disconnects.

Components receive presentation-shaped payloads such as chart data, table windows, filters, project asset summaries, access state, refresh progress, or chat messages. They do not receive a database connection or semantic planner.

## Initial dashboard update

A report page establishes the update flow for its dashboard/page identity. Go resolves canonical filters and selections, plans required consumers, executes bounded queries, and patches status plus result signals.

Chart and table hosts bind to their result paths. A slow component can be patched independently as results become available, while generation identity prevents older results from replacing a later interaction.

## Commands

Components emit small commands for actions such as:

- changing or clearing a filter;
- selecting a chart datum;
- selecting a table row;
- requesting another table window;
- refreshing a project asset;
- changing access configuration;
- submitting an agent turn.

The command includes stable route/component identity and typed values. Go validates the principal, resource, mapping, generation, and operation before changing canonical state or executing work.

Do not add an ad hoc component `fetch()` for dashboard data when the operation belongs to this lifecycle. A second transport path tends to duplicate auth handling, cancellation, loading state, and payload contracts.

## Command failure and recovery

Every browser command has two coordinated failure paths:

- A handler that understands a domain failure patches the feature's typed error/status signal. It retains the last known-good payload and returns controls to an idle state in the same patch.
- A terminal transport failure that cannot deliver a domain patch is classified by `browserCommandFailure` from `web/components/shared/command-failure.ts`. The component's `datastar-fetch` listener clears its local busy key and renders a durable `role="alert"` or `role="status"` recovery state.

The shared classifier covers validation and authentication failures, forbidden or missing resources, revision conflicts, rate limits, unavailable services, timeouts, disconnects, and Datastar retry exhaustion. Messages remain generic where more detail would disclose a protected resource or an internal error.

Recovery is explicit. A read may offer **Retry** or **Reload**. A write may only offer an automatic retry when its operation has a stable idempotency key and the server retains replay evidence; otherwise the UI explains that the result is unknown and offers a canonical reload. Components must never replay a failed write merely because the transport reconnects.

Feature owners implementing a command should therefore:

1. keep the last successful signal payload separate from loading/error state;
2. set a bounded busy key before dispatch and clear it on success, typed domain failure, and terminal `datastar-fetch` failure;
3. use generated command bindings and operation claims, including CSRF and request identity;
4. render the failure in an accessible live region with a keyboard-reachable safe action;
5. preserve 401, 403, 404, 409, 429, and 5xx semantics instead of translating them into success patches;
6. test the positive command, every relevant failure class, retry exhaustion, control re-enablement, retained inputs, and the absence of blind replay.

Data Explorer, dashboard authoring, project connection/pipeline administration, and the administration surfaces are reference implementations. The shared classifier's unit suite is the taxonomy contract; each surface also owns DOM coverage for its state retention and recovery actions.

## Optimistic state

Optimistic feedback can make selection feel immediate. It must remain temporary presentation state keyed to the latest command. When server canonical state arrives, the component reconciles to it.

Rapid changes must replace older optimistic state. A rejected command restores or reports canonical state; it must not leave a browser-only selection that the server never applied.

## One-shot and long-lived responses

Use a bounded one-shot Datastar response when all patches are produced by the current command. Close it after delivery. Use a long-lived stream only when a registered publisher can send meaningful later events, such as coordinated dashboard result delivery.

Leaving one-shot responses open consumes connections and obscures completion. Conversely, closing a true asynchronous stream before its publisher runs loses events. The handler contract should make the distinction explicit.

## Cancellation and generations

Request contexts propagate cancellation to query work. Stream coordinators associate results with refresh generations. A new filter or selection can supersede prior work; cancelled or late results are ignored according to generation rules.

Refresh jobs use analogous generation protection so an older run cannot activate after a newer request has become authoritative.

## Contract rules

- Define signal names and payloads centrally or generate them.
- Keep route bootstrap state minimal and typed.
- Patch focused roots rather than replacing unrelated state.
- Preserve scalar types; false, zero, null, and strings are distinct.
- Validate semantic selection mappings on the server.
- Model loading, empty, error, and terminal state explicitly.
- Dispose browser observers and renderer instances on disconnect.
- Test cold hydration, rapid supersession, cancellation, and response completion.

This boundary keeps browser components replaceable while security, semantic resolution, and lifecycle behavior remain in Go.
