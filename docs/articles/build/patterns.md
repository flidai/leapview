# Dashboard authoring patterns

These patterns keep a dashboard repository understandable as the number of project resources, semantic definitions, and report pages grows. They are review heuristics rather than substitutes for the generated contracts.

## Model trustworthy data

### Model once, present many times

Put reusable business calculations in semantic measures and metrics. Dashboard YAML should select and present them, not redefine them. When two visuals need “Revenue,” both should request the same measure unless their business meaning genuinely differs.

If a formula changes, one semantic edit should update browser dashboards, headless API clients, and agent queries together. Add a separate named measure when two interpretations must coexist.

### Keep grains explicit

Give each model table a one-sentence grain and primary key. Review joins for how they change that grain. A one-to-many join performed before aggregation is a common source of inflated sums and counts.

Semantic measures should name their fact table, and relationships should declare only cardinality verified from data. If a question spans incompatible facts, create an explicit analytical table or redesign the question rather than relying on a convenient join.

### Prefer bounded, deterministic results

Set chart limits, option limits, table windows, and deterministic sorts. A query without a limit or stable order may appear correct on sample data and fail unpredictably at production cardinality.

Use ascending time order for trends and explicit value order for rankings. Decide how ties should appear. Keep filter option lists small enough to be useful; a selector with thousands of values is usually the wrong interaction.

## Preserve stable behavior

### Keep identity stable

Resource names, semantic IDs, dashboard definition IDs, page component IDs, filter URL parameters, and interaction targets form an operational contract. They appear in routes, saved links, API requests, tests, and selection state.

Change labels freely when meaning is preserved. Rename identifiers only with a repository-wide search, a compatibility decision, and a reviewed deployment plan.

### Separate data logic from layout

Visual definitions own queries. Page entries own composition and placement. This keeps a layout review focused on the canvas and a semantic review focused on business behavior.

Avoid copying the same query under several page components. Reuse a dashboard definition where its query and presentation intent are the same; create a distinct definition when options or interactions genuinely differ.

### Design for empty and partial state

Every component should have understandable loading, empty, and failure behavior. A filter combination that yields no rows is normal, not exceptional. Measures should declare empty behavior, charts should explain the absence of points, and tables should retain their column context.

Do not let one component failure make the entire page unreadable when the rest of the state is still valid.

### Make interactions local and explainable

Explicitly target filters and selections when broad behavior would surprise users. Add one interaction at a time and test it with existing filter state. Users should be able to see and clear a selection.

Semantic mappings should preserve types and fact/grain identity. Never treat a browser label or row index as durable query identity.

## Make ownership reviewable

### Document ownership and intent

Use titles, descriptions, owners, and tags to make discovery and review easier. Add descriptions to non-obvious measures, relationships, transformations, filters, and dashboards. A future maintainer should understand why a resource exists without reconstructing the original ticket.

Keep operational runbooks in documentation and exact machine contracts in generated references. Do not maintain a second hand-written field list that can drift from the schema.

### Validate in layers

Use this review sequence:

1. Configuration validation for syntax, discovery, and references.
2. Model-table materialization checks for grain, keys, and types.
3. Semantic preview, explain, and query checks for business values.
4. Component checks for shape, sorting, limits, and empty state.
5. Browser checks for layout, accessibility, and interactions.
6. Target-aware plan review before activation.

Commit project resources, source contract changes, public contract snapshots, tests, and authored documentation together when behavior changes. Build-only generated code and reference pages are recreated by `task generate` and remain outside Git. This keeps review focused on authoritative inputs without losing externally consumed schema or OpenAPI diffs.
