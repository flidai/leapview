# Core concepts

LeapView separates physical data access, reusable analytical state, business meaning, dashboard presentation, ownership, and serving state. Use these concept pages to understand a boundary before applying it in a task-oriented guide.

## Organize the project

- [Projects and environments](/docs/concepts/projects-environments) explains configuration ownership, authorization scope, and atomic delivery.
- [Connections and sources](/docs/concepts/connections-sources) separates physical access from stable reusable input definitions.
- [Managed data and revisions](/docs/concepts/managed-data) explains immutable file revisions, staging, activation, upload boundaries, and storage responsibility.

## Build analytical meaning

- [Model tables](/docs/concepts/model-tables) explains materialized analytical grain, keys, transformations, refresh, and activation.
- [Semantic models](/docs/concepts/semantic-models) explains dimensions, metrics, relationships, and governed query meaning.

## Present and serve results

- [Dashboards, pages, and visuals](/docs/concepts/dashboards) explains reusable report definitions, filters, layouts, interactions, and renderer-neutral visuals.
- [Query and interaction lifecycle](/docs/concepts/query-lifecycle) follows a browser action through authorization, semantic planning, DuckDB execution, cancellation, and streamed signal delivery.

Continue with [Build dashboards](/docs/guides/build) when these boundaries are familiar, or start with [Get started with LeapView](/docs/getting-started) for the guided beginner path.
