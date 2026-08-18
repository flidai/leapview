# Table

Use a table when readers need exact record-level values, sorting, and a virtualized window over a governed result set.

{{< visual id="orders_table" >}}

```yaml visual-example=orders_table
visuals:
  orders_table:
    type: table
    title: Orders
    query:
      type: records
      dataset: orders
      fields:
      - order_id
      - status
      - revenue
    presentation:
      type: table
      rowHeight: 32
      showHeader: true
      striped: false
```
