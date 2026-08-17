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
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        order_count: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 30
```

## Stacked series

Map status through `query.series` and enable `presentation.stacked` to show both the monthly total and each status contribution.

{{< visual id="orders_by_month_status" >}}

```yaml visual-example=orders_by_month_status
visuals:
  orders_by_month_status:
    title: Orders by month and status
    description: Compares monthly order volume split by status.
    type: column
    presentation:
      stacked: true
    query:
      dimensions:
        purchase_month: orders.purchase_month
      series:
        field: orders.status
        alias: status
      metrics:
        order_count: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 40
```

## Grouped series

Keep the series unstacked to place statuses side by side, and use `presentation.legend` to position the series key above the plot.

{{< visual id="orders_by_month_status_grouped" >}}

```yaml visual-example=orders_by_month_status_grouped
visuals:
  orders_by_month_status_grouped:
    title: Orders by month and status grouped
    type: column
    presentation:
      legend: bottom
    query:
      dimensions:
        purchase_month: orders.purchase_month
      series:
        field: orders.status
        alias: status
      metrics:
        order_count: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 60
```
