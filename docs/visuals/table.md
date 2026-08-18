# Table

Use a table when readers need exact record-level values, sorting, and a virtualized window over a governed result set.

{{< visual id="orders_table" >}}

```yaml visual-example=orders_table
visuals:
  orders_table:
    type: table
    title: Orders
    cardinality: exact
    default_sort:
      key: revenue
      direction: desc
    columns:
      - key: order_id
        label: Order
      - key: status
        label: Status
      - key: revenue
        label: Revenue
        align: right
        format: currency
    query:
      dataset: orders
      fields:
        - orders.order_id
        - orders.status
        - orders.revenue
```
