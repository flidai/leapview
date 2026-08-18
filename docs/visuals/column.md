# Column chart

Use a column chart to compare categories or ordered periods with vertical bars.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic

Use one ordered category and one metric for a direct vertical comparison. Ascending month order makes changes over time easy to scan.

{{< visual id="orders_by_month_column" >}}

```yaml visual-example=orders_by_month_column
visuals:
  orders_by_month_column:
    title: Orders by month
    type: column
    query:
      type: aggregate
      dimensions:
      - purchase_month
      metrics:
      - order_count
      sort:
      - field: purchase_month
        direction: asc
      limit: 30
    presentation:
      type: cartesian
```
