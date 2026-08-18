# Headless BI queries

Headless BI operations expose the same active dashboards and semantic models used by the browser project catalog. They apply active deployment state, authorization, data policies, semantic relationships, query bounds, and DuckDB execution without accepting unrestricted SQL from the caller.

## Choose dashboard or semantic access

Use dashboard operations when an integration should reproduce a reviewed report experience: its filters, component identities, visual shapes, table configuration, sorting, and limits.

Use semantic-model operations when an integration has its own presentation but should reuse governed dimensions, metrics, datasets, and relationships.

Do not copy generated SQL out of an explain response and turn it into a second integration contract. Explain is for diagnosis; semantic requests are the stable input.

## Discover before querying

For dashboards, discover in this order:

```sh
leapview dashboards list --project project:commerce
leapview dashboards describe executive-sales --project project:commerce
leapview dashboards page executive-sales overview --project project:commerce
leapview dashboards filter executive-sales overview state --project project:commerce
```

Add `--target` and `--token`, or configure the corresponding environment values. Discovery returns stable IDs and supported components from the active deployment.

For semantic models:

```sh
leapview semantic-models list --project project:commerce
leapview semantic-models describe sales --project project:commerce
leapview semantic-models datasets sales --project project:commerce
leapview semantic-models fields sales orders --project project:commerce
```

Cache catalog metadata for a bounded interval, but rediscover after project deployment. A removed or renamed field should fail explicitly rather than being silently substituted by title.

## Query a report page

Use a coordinated page query when several components should share one canonical filter state:

```sh
leapview dashboards query-page executive-sales overview \
  --project project:commerce \
  --filter-state-json '{"version":"typed_v1","controls":{"fb_REPLACE_WITH_DISCOVERED_KEY":{"kind":"set","operator":"in","values":[{"kind":"string","value":"SP"}]}}}'
```

Use the opaque `binding.key` returned by `dashboards filter`; do not derive it. The exact filter JSON follows the dashboard query contract. Querying the page can be preferable to issuing several unrelated component calls because the server can resolve shared state coherently.

Use visual or table data commands for focused refreshes. Table requests support bounded windows/count according to their generated command and API contracts.

## Query a semantic dataset

Preview row-level data to understand fields, then send an aggregate query for dimensions and metrics. Use a body file for non-trivial JSON:

```sh
leapview semantic-models preview sales orders \
  --project project:commerce \
  --body-json '{"limit":10}'

leapview semantic-models query sales \
  --project project:commerce \
  --body-file ./query.json
```

Inspect `explain-preview` or `explain-query` when resolution fails or a query is unexpectedly expensive. Explanations can reveal sensitive model structure; protect them with the same access discipline as query metadata.

## Bound and validate results

Always provide supported limits, stable sorting, and pagination/window state. Validate response shape, semantic field IDs, scalar types, null handling, and deployment context before using a result in downstream automation.

Aggregates can change after a refresh or deployment even when the request is identical. Record environment, active project/deployment identity where available, and query time when reproducibility matters.

## Security

Data policies apply to headless requests as they do to dashboards. A caller must not infer that catalog visibility grants query privilege. Use a read-only service principal scoped to the project and operations required.

Avoid placing sensitive result bodies in general application logs. Audit metadata and correlation IDs are usually sufficient for operations.

Start with the generated [BI API](/docs/api/bi), [`dashboards` CLI group](/docs/cli/dashboards), and [`semantic-models` CLI group](/docs/cli/semantic-models).
