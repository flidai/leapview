# Matrix

Use a matrix for grouped rows and metrics, optionally split across a column dimension.

{{< visual id="status_matrix" >}}

```yaml visual-example=status_matrix
visuals:
  status_matrix:
    type: matrix
    title: Orders by category and status
    query:
      type: pivot
      rows:
      - category
      columns:
      - status
      metrics:
      - order_count
      - revenue
    presentation:
      type: table
      rowHeight: 32
      showHeader: true
      striped: false
```
