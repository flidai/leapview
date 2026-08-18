# Pivot

Use a pivot for a compact cross-tab with one row dimension, one column dimension, and one metric.

{{< visual id="category_pivot" >}}

```yaml visual-example=category_pivot
visuals:
  category_pivot:
    type: pivot
    title: Order count by category and status
    query:
      rows:
        category: orders.category
      columns:
        status: orders.status
      metrics:
        order_count: null
```
