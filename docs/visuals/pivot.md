# Pivot

Use a pivot for a compact cross-tab with row dimensions, column dimensions, and metrics.

{{< visual id="category_pivot" >}}

```yaml visual-example=category_pivot
visuals:
  - id: category_pivot
    type: pivot
    title: Order count by category and status
    query:
      type: pivot
      rows:
      - category
      columns:
      - status
      metrics:
      - order_count
    presentation:
      type: table
      rowHeight: 32
      showHeader: true
      striped: false
```

Conditional-format targets may name the visible row field or metric alias.
Pivot column dimensions generate headers and are not visible target columns.

Pivots support the same expandable row hierarchies as
[matrices](./matrix.md#expandable-row-hierarchies). Add
`presentation.hierarchy` for dimension levels, parent-child accounts, or nested
children. Expand/collapse changes the visible rows without rerunning the query.
