# Create a dashboard

A dashboard chooses one project semantic model and composes reusable filters, visual queries, tabular queries, and report pages. Build the smallest useful page first, verify its query behavior, and add interactions only after standalone results are correct.

> [!TIP]
> Use this guide for the authoring workflow and representative configuration. Use [Dashboard configuration](/docs/config/dashboard) and [Visual types](/docs/visuals/overview) for the complete accepted field contract.

## Before you begin

Verify the semantic model with direct queries and choose a small decision-oriented page. Prepare expected values for each initial visual at an unfiltered state and at least one filtered state.

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
  layout:
    columns: 12
    rowHeight: 48
    gap: 16
    padding: 16
  filters: []
  visuals:
    revenue-by-month:
      title: Revenue by month
      type: area
      query:
        type: aggregate
        dimensions: [purchase_month]
        metrics: [revenue]
        sort:
          - field: purchase_month
            direction: asc
        limit: 30
      presentation:
        type: cartesian
    total-revenue:
      title: Total revenue
      type: kpi
      query:
        type: aggregate
        dimensions: []
        metrics: [revenue]
      presentation:
        type: kpi
        displayUnits: auto
  pages:
    - id: overview
      title: Overview
      components:
        - id: revenue-trend
          type: visual
          visual: revenue-by-month
          placement: {column: 1, row: 1, columnSpan: 12, rowSpan: 8}
        - id: revenue-kpi
          type: visual
          visual: total-revenue
          placement: {column: 1, row: 10, columnSpan: 3, rowSpan: 3}
```

The visual definition owns the semantic query and presentation. The page entry references it by stable ID and owns placement. This separation keeps layout edits from rewriting data logic.

### Design the query result

Dimension and metric selections are ordered sequences of semantic members. The names delivered to a result frame are the member names unless an explicit typed alias is used. Sort fields address those result names, never source expressions.

Every chart query should have a bounded limit and deterministic sort. For time series, sort the time field ascending. For ranked bars, sort the value descending and choose a limit readers can scan. Do not rely on database default order.

### Add a KPI

KPI visuals use one metric and a typed KPI presentation:

```yaml
visuals:
  total-orders:
    type: kpi
    query:
      type: aggregate
      dimensions: []
      metrics: [order_count]
    presentation:
      type: kpi
      displayUnits: auto
```

`displayUnits: auto` chooses one shared magnitude for the complete KPI context. Use `none` for canonical unscaled semantic formatting or a fixed unit when comparable cards must retain a shared scale. Tooltips and detail surfaces keep exact values.

### Add filters after the base query works

Define filters against semantic fields and place typed filter components on the page. Exercise each filter independently before combining several. Use stable URL parameters when users should share filtered links.

## Validate the dashboard

Ensure the project manifest includes dashboard files, then run:

```sh
leapview validate --project dashboards/leapview.yaml
leapview plan dashboards/leapview.yaml
```

Validation checks contract shape and references. The plan shows target-owned impact and source-attestation evidence. Build the reviewed plan and verify the rendered page with representative data before publishing the sealed candidate.

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
