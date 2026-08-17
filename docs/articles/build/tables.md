# Tables, matrices, and pivots

Use tabular visuals when exact values, comparison across several fields, or record-level inspection matters more than visual pattern recognition. Tables, matrices, and pivots use the same visual contract and namespace as charts and KPIs.

## Choose a table shape

### Data tables

A data table selects fields from a model-table grain:

```yaml
visuals:
  orders_table:
    type: table
    title: Orders
    description: Recent order records.
    cardinality: bounded
    query:
      dataset: orders
      fields:
        - orders.order_id
        - orders.purchase_date
        - orders.status
        - orders.revenue
    default_sort:
      key: purchase_date
      direction: desc
    columns:
      - {key: order_id, label: Order, width: 220}
      - {key: purchase_date, label: Purchased, width: 130}
      - {key: status, label: Status, width: 120}
      - {key: revenue, label: Revenue, align: right, format: currency}
```

Select only fields needed for the task. A stable default sort is essential because windowed delivery otherwise produces an arbitrary first page. Column keys must match delivered result keys.

Use `cardinality: bounded` unless the workflow explicitly requires exact global counts and the associated cost is acceptable. Browser tables should load windows rather than one unbounded result.

### Matrices

Matrices group semantic metrics by row and optional column dimensions:

```yaml
visuals:
  state_status_matrix:
    type: matrix
    title: Orders and revenue by state and status
    query:
      rows:
        state: customers.state
      columns:
        status: orders.status
      metrics:
        order_count:
        revenue:
```

Use a matrix for a stable multidimensional comparison with known cardinality. High-cardinality row and column combinations create a sparse, unreadable surface and expensive result. Filter or remodel the question instead of relying on horizontal scrolling.

### Pivots

A pivot uses the same row, column, and metric concepts but emphasizes analytical rearrangement:

```yaml
visuals:
  category_status_pivot:
    type: pivot
    title: Orders by category and status
    query:
      rows:
        category: orders.category
      columns:
        status: orders.status
      metrics:
        order_count:
```

Keep the initial pivot shape useful and bounded. A pivot is not a substitute for an unconstrained query builder; its available fields still come from the dashboard and semantic contracts.

## Add table behavior

### Formatting

Semantic metric formatting supplies a good default. Table columns and metric-formatting rules can add table-specific alignment, labels, widths, badges, text colors, background scales, or data bars.

Formatting must not be the only way a value is communicated. Use readable text and numeric formatting alongside color. Choose explicit scale bounds when comparable pages must use the same visual meaning; otherwise users may misread two differently scaled cells as equivalent.

### Row selections

Data-table rows can emit semantic selections when mappings identify delivered values, semantic fields, datasets, and targets. Do not send an entire record as an implicit filter. Map only the values the server needs and verify that selected rows remain identifiable when the loaded window changes.

## Place and test the table

Place any table definition with a page component:

```yaml
- id: order-details
  kind: visual
  visual: orders_table
  placement: {col: 1, row: 12, col_span: 12, row_span: 8}
```

Test sorting, loading another window, column state, compact widths, null values, empty results, and row selections. Compare aggregate matrix values with direct semantic queries and confirm data-table rows preserve the declared model-table grain.

The full table, query, column, formatting, and interaction fields are generated in [Dashboard configuration](/docs/config/dashboard).
