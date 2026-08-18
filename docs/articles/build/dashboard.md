# Create a dashboard

A dashboard chooses one project semantic model and composes reusable filters, visual queries, tabular queries, and report pages. Build the smallest useful page first, verify its query behavior, and add interactions only after the standalone results are correct.

> [!TIP]
> Use this guide for the authoring workflow and representative configuration. Use [Dashboard configuration](/docs/config/dashboard) and [Visual types](/docs/visuals/overview) when you need the complete accepted field contract.

## Before you begin

Verify the semantic model with direct queries and choose a small decision-oriented page to build first. Prepare expected values for each initial visual at an unfiltered state and at least one filtered state.

Use this sequence:

1. Create the dashboard and one bounded, deterministically sorted visual query.
2. Place that visual on a page with a compact-layout reading order.
3. Add a KPI and verify both against direct semantic queries.
4. Add filters and interactions one at a time.
5. Validate, plan, deploy to development, and review every state.

## Define the dashboard surface

### Create the resource

Create `dashboards/dashboards/executive-sales.yaml`:

```yaml
apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:executive-sales
  name: executive-sales
  displayName: Executive Sales
  description: Revenue and order trends for sales leadership.
  tags: [sales, revenue]
spec:
  semanticModel: sales
  visuals:
    revenue_by_month:
      title: Revenue by month
      type: area
      query:
        dimensions:
          purchase_month: orders.purchase_month
        metrics:
          revenue:
        sort:
          - field: purchase_month
            direction: asc
        limit: 30
  pages:
    - id: overview
      title: Overview
      grid:
        columns: 12
        row_height: 48
        gap: 16
        padding: 16
      components:
        - id: revenue-trend
          kind: visual
          visual: revenue_by_month
          placement: {col: 1, row: 1, col_span: 12, row_span: 8}
```

The visual definition owns semantic query and presentation settings. The page entry references it by stable ID and owns placement. This separation keeps layout edits from rewriting data logic.

### Design the query result

Names on the left of `dimensions` and `metrics` are stable field IDs compiled into the visualization specification. Values on the right refer to semantic fields. Choose clear aliases and keep them stable when typed presentation or interactions depend on them.

Every chart query should have a bounded limit and deterministic sort. For time series, sort the time field ascending. For ranked bars, sort the value descending and choose a limit that users can read. Do not rely on database default order.

### Add a KPI

KPI visuals use one metric and typed KPI presentation:

```yaml
visuals:
  total_revenue:
    type: kpi
    query:
      metrics:
        revenue:
    presentation:
      display_units: auto
      note: Filtered order revenue
      tone: success
```

Place it on the page with `kind: visual` and `visual: total_revenue`. Its `type: kpi` selects the KPI renderer. `display_units: auto` is the default and chooses one shared magnitude with at most three significant digits for the complete KPI context. Use `none` for canonical unscaled semantic formatting or force `thousands`, `millions`, `billions`, or `trillions` when comparable cards must retain a fixed scale. Tooltips and detail surfaces keep exact values.

### Add filters after the base query works

Define filters against semantic fields and place filter-card components on the page. Exercise each filter independently before combining several. Use stable URL parameters when users should share filtered links.

## Validate the dashboard

### Discover and validate

Ensure the project manifest includes dashboard files, then run:

```sh
leapview validate --project dashboards/leapview.yaml
leapview plan dashboards/leapview.yaml
```

Validation checks contract shape and references. The plan shows target-owned
impact and source-attestation evidence. Build the reviewed plan and verify the
rendered page with representative data before publishing the sealed candidate.

## Verify the rendered page

Confirm that:

- the dashboard appears in the intended project resource catalog;
- titles, descriptions, and tags support discovery;
- chart and KPI results match direct semantic queries;
- filters change every intended component and no unintended component;
- empty, loading, and failure states are readable;
- component order makes sense for keyboard and compact layouts;
- limits and sorting remain useful for high-cardinality data.

## Troubleshooting

If a visual is empty, first run its semantic query without dashboard filters, then add filters one at a time. If values are correct but order changes between loads, add an explicit sort with a stable tie-breaker. If a compact layout reads poorly, fix component source order and placement together rather than using visual-only CSS reordering.

## Next steps

Continue with [Pages and layout](/docs/guides/build/pages-layout), [Filters and interactions](/docs/guides/build/filters-interactions), and [Tables, matrices, and pivots](/docs/guides/build/tables). Use [Dashboard configuration](/docs/config/dashboard) and [Visual types](/docs/visuals/overview) for exact contracts.
