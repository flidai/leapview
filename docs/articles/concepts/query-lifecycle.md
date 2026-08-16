# Query and interaction lifecycle

LeapView keeps query state, semantic resolution, authorization, and data access on the server. The browser renders typed presentation state and sends small commands; it does not receive credentials or construct unrestricted SQL.

## Initial page request

When a user opens a report page:

1. The HTTP route resolves the requested project, dashboard, and page against the instance environment.
2. Authorization confirms that the principal can view the resource.
3. Go renders the document shell, page component hosts, and initial Datastar signal contract with gomponents.
4. Lit custom elements connect to the signal paths they render.
5. The page establishes the update stream required for initial and subsequent patches.

The initial HTML provides stable structure and bootstrap state. Data-dependent components can then receive focused updates without replacing the entire document.

## Query resolution

For each required result, the server combines:

- the active project and managed-data revision;
- project-resource authorization;
- the dashboard's semantic model;
- page filter values and operators;
- validated visual or table selections;
- the visual, KPI, filter-option, or table query contract;
- result limits, sort order, and table window state.

Semantic names are resolved to known model-table fields, relationships, aggregations, and expressions. Data policies are applied at the server boundary. DuckDB executes the resulting bounded work against active analytical state.

## Signal delivery

The result is converted into LeapView-owned payloads rather than renderer-specific database rows. Go patches KPI, chart, table, filter-option, page, and status signals. Each Lit component observes its configured signal path and rerenders only when relevant state changes.

The signal contract is part of the application architecture. A chart renderer may change without changing how filters are authorized or how the semantic query is defined.

## User commands

When a user changes a date range, selects a chart point, clicks a table row, requests another table window, or starts a refresh, the component emits a small command with typed values and stable component identity.

The server then:

1. validates the command and resource identity;
2. normalizes values into canonical state;
3. determines which queries are affected;
4. cancels or supersedes obsolete work where applicable;
5. executes the new bounded queries;
6. patches canonical state and results back to the page.

Optimistic UI may provide immediate feedback, but server state remains authoritative. A late result from an older generation must not overwrite a newer selection or refresh.

## Cancellation and failures

Request cancellation should propagate into query work. Rapid interactions can supersede earlier work, and disconnected clients should not keep unnecessary queries alive. Components receive explicit loading, empty, or error state rather than inferring success from missing data.

A failed component query should remain scoped where possible: the page shell and unrelated state can continue to render while the affected result reports its failure. Authorization failures, invalid commands, and malformed semantic references are rejected before unrestricted work reaches DuckDB.

## Why this boundary matters

Keeping the lifecycle server-owned provides four durable properties:

- one authorization and data-policy boundary for browser and headless access;
- one semantic interpretation of fields and measures;
- deterministic cancellation and supersession behavior;
- renderer plugins that remain replaceable presentation adapters.

Read [Datastar signal flow](/docs/architecture/datastar) for implementation details and [Headless BI queries](/docs/guides/integrate/headless-bi) for the equivalent non-browser surfaces.
