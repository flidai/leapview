# Dashboards, pages, and visuals

A dashboard is a project presentation resource backed by one semantic model. It defines reusable filters, visuals, and one or more report pages that place those definitions on a canvas.

## Dashboard identity

The dashboard's `metadata.name` is its stable route and resource identity. `metadata.title`, description, and tags support discovery. `spec.semanticModel` selects the semantic vocabulary available to every query in the dashboard.

Keep a dashboard focused on one coherent audience or decision flow. Separate dashboards can reuse the same semantic model without copying business definitions.

## Filters

Filters expose semantic fields as page controls. A filter definition chooses a control type, field, operator behavior, URL parameters, defaults, and—where applicable—how option values are obtained.

Selected values become server-owned query state. Relevant KPI, chart, table, and filter-option queries are recomputed from the same canonical values. URL parameter names make useful report state shareable; change them carefully because saved links may depend on them.

Date ranges, multi-selects, and text filters have different value and operator contracts. Use the generated [Dashboard configuration](/docs/config/dashboard) rather than assuming options are interchangeable between types.

## Visual definitions

A visual definition pairs a closed visual type with a semantic query and typed presentation. The compiler resolves it into a renderer-independent `VisualizationSpec`. For example, a chart query names stable field aliases for dimensions and measures, declares sort order, and sets a result limit. A KPI query requests a single measure or metric.

Visual IDs are local reusable identities inside the dashboard. A page component references the visual by ID:

```yaml
visuals:
  revenue_by_month:
    title: Revenue by month
    type: area
    query:
      dimensions:
        purchase_month: orders.purchase_month
      measures:
        revenue:
      limit: 30
```

The browser host receives a versioned visualization envelope; it does not parse semantic expressions or execute SQL. See the [visual reference](/docs/visuals/overview) for supported types and focused examples.

## Tables, matrices, and pivots

Tabular definitions have their own query and interaction contract. A data table typically selects row-level fields from a model table. Matrices and pivots organize dimensions and values for analytical comparison. LeapView keeps the server query and signal contract while TanStack supplies internal browser interaction behavior such as windowing and column state.

Always bound tabular results. Large exports and analytical exploration require an explicit headless or windowed workflow; a report page should not render an unbounded table.

## Pages and layout

Pages divide a dashboard into focused report surfaces. Each page declares canvas and grid settings and places components using row, column, span, and stable component IDs.

Page components reference filters, visuals, or headers. Every chart, KPI, table, matrix, and pivot uses `kind: visual`; the referenced visual's `type` selects its renderer. This separation lets the same visual query move on a page without being redefined.

Design the page in reading order as well as visual order. Stable IDs, meaningful titles, and a predictable grid improve keyboard navigation, testing, and future migrations.

## Interactions

Selections are semantic mappings rather than arbitrary browser-only filters. A chart point or table row can emit typed field values associated with a fact and grain. The server validates those mappings and decides which targets should update.

This prevents a renderer from inventing query behavior. It also allows the same selection contract to work across renderer plugins and server refreshes. Define targets narrowly and test how a selection composes with page filters and existing selections.

## Validation strategy

Review a dashboard in layers:

1. Validate resource syntax and references.
2. Query the semantic fields independently.
3. Verify each visual payload.
4. Check page layout at desktop and compact widths.
5. Exercise filters, selections, empty results, and error states.

Continue with [Create a dashboard](/docs/guides/build/dashboard), [Pages and layout](/docs/guides/build/pages-layout), and [Filters and interactions](/docs/guides/build/filters-interactions).
