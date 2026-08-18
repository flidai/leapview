# Tables, matrices, and pivots

Use tabular visuals when exact values, comparison across several fields, or record-level inspection matters more than visual pattern recognition. Tables, matrices, and pivots use the same governed visual contract as charts and KPIs.

## Choose a table shape

### Data tables

A records query selects fields from a model-table grain:

```yaml
visuals:
  orders_table:
    type: table
    title: Orders
    description: Recent order records.
    query:
      type: records
      dataset: orders
      fields: [order_id, purchase_date, status, revenue]
      sort:
        - field: purchase_date
          direction: desc
      limit: 100
    presentation:
      type: table
```

Select only fields needed for the task. A deterministic sort and bounded limit make the first window stable. Record fields are unqualified root fields; semantic relationships are resolved by the dataset contract.

### Matrices

Matrices group semantic metrics by row and optional column dimensions:

```yaml
visuals:
  state_status_matrix:
    type: matrix
    title: Orders and revenue by state and status
    query:
      type: pivot
      rows: [customer_state]
      columns: [status]
      metrics: [order_count, revenue]
    presentation:
      type: table
```

Use a matrix for a stable multidimensional comparison with known cardinality. High-cardinality row and column combinations create a sparse, unreadable surface and an expensive result.

### Pivots

A pivot uses the same row, column, and metric concepts but emphasizes analytical rearrangement:

```yaml
visuals:
  category_status_pivot:
    type: pivot
    title: Orders by category and status
    query:
      type: pivot
      rows: [category]
      columns: [status]
      metrics: [order_count]
    presentation:
      type: table
```

Keep the initial pivot shape useful and bounded. A pivot is not an unrestricted query builder; its available fields still come from the dashboard and semantic contracts.

## Add table behavior

Semantic metric formatting supplies a good default. Use table-specific presentation metadata only when the field is already part of the governed result. Formatting must not be the only way a value is communicated: readable text and numeric formatting remain available alongside color.

Data-table rows can emit semantic selections when mappings identify delivered values, semantic fields, datasets, and targets. Do not send an entire record as an implicit filter.

## Place and test the table

Place a table definition with a typed page component:

```yaml
components:
  - id: order-details
    type: visual
    visual: orders_table
    placement: {column: 1, row: 12, columnSpan: 12, rowSpan: 8}
```

Test sorting, loading another window, compact widths, null values, empty results, and row selections. Compare aggregate matrix values with direct semantic queries and confirm data-table rows preserve the declared model-table grain.

The full table, query, and interaction fields are generated in [Dashboard configuration](/docs/config/dashboard).
