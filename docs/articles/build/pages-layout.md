# Pages and layout

Pages arrange reusable dashboard components on a deterministic responsive grid. Query definitions remain at dashboard scope; the page decides which filters and visuals appear together and where they are placed.

## Build the page structure

### Define dashboard grid defaults

Grid defaults belong to the dashboard spec and use camel-case fields:

```yaml
layout:
  columns: 12
  rowHeight: 48
  gap: 16
  padding: 16
```

A page can override only the values that need to differ:

```yaml
pages:
  - id: overview
    title: Overview
    description: Revenue and order performance at a glance.
    layout:
      columns: 12
      rowHeight: 48
      gap: 16
      padding: 16
    components: []
```

The grid creates predictable coordinates and spans. Use a consistent column count, gap, and row height across related pages so readers do not experience a different visual rhythm on each route.

### Place components

Each page component has a stable ID, a typed component discriminator, exactly the relevant reference, and a camel-case placement:

```yaml
components:
  - id: revenue
    type: visual
    visual: revenue-kpi
    placement: {column: 1, row: 1, columnSpan: 3, rowSpan: 3}
  - id: revenue-trend
    type: visual
    visual: revenue-by-month
    placement: {column: 4, row: 1, columnSpan: 9, rowSpan: 8}
  - id: order-details
    type: visual
    visual: orders-table
    placement: {column: 1, row: 10, columnSpan: 12, rowSpan: 8}
```

Use `type: visual` for charts, KPIs, tables, matrices, and pivots. Use `type: filter` for an on-page filter presentation and `type: header` for headings. A filter component references the dashboard filter ID; it does not define a second predicate system.

Coordinates are one-based. Keep `column + columnSpan - 1` within the configured column count and avoid accidental overlaps.

### Design reading order

YAML order should follow the intended document and keyboard order, not just visual coordinates:

1. page context or header;
2. high-value filters;
3. summary KPIs;
4. primary analytical visual;
5. supporting comparisons;
6. record-level or multidimensional detail.

Coordinates control desktop placement while source order communicates meaning and provides a sensible compact-layout fallback.

### Size for content

Choose spans based on what a component must display. KPI cards need width for formatted values, legends and long category labels need chart width, time-series charts need horizontal space, and tables need enough height for a useful initial window. Filter components need room for selected values, summaries, search, and operators.

LeapView applies explicit features, automatic layout, and enforced minimums. Renderers may rearrange content at narrower widths, but they do not hide an explicit feature or change a number format. A placement is rejected when no valid internal arrangement fits.

Do not solve overcrowding by shrinking every component. Split a page when readers are expected to answer distinct questions or when detail pushes primary analysis below several screenfuls.

## Stabilize and test the layout

### Keep IDs stable

Page component IDs participate in interaction targeting and client state. Renaming `revenue-trend` can break a selection target even when the referenced visual remains unchanged. Treat IDs as local API names: change them intentionally and search the dashboard for references.

Visual definition IDs should also remain stable. Moving a component requires only a placement edit; it should not require copying or renaming its query.

### Test layouts

After deployment to development:

- inspect the page at the configured desktop width;
- test a compact browser width and the mobile navigation shell;
- check long titles, large currency values, and empty states;
- use keyboard navigation to confirm source order is understandable;
- verify charts resize without clipped labels or legends;
- confirm tables remain usable without document-wide horizontal scrolling.

Use the sample Sales dashboard and visual showcase as working layout examples. See [Dashboard configuration](/docs/config/dashboard-document) for the current page and placement contract.
