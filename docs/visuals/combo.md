# Combo chart

Use a combo chart when related metrics need different visual encodings.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Multiple metrics

Select multiple `query.metrics` to render related values against the same category axis. The combo shape assigns each metric its own series; this reference explicitly hides direct labels so the line remains readable against the columns.

{{< visual id="revenue_orders_combo" >}}

```yaml visual-example=revenue_orders_combo
visuals:
  revenue_orders_combo:
    title: Revenue and orders by month
    description: Compares monthly revenue and order volume together.
    type: combo
    presentation:
      labels: {density: hidden, priority: [], max_characters: 24, minimum_spacing: 0, tooltip_fallback: true}
      dual_axis: true
      series_types:
        Revenue: line
        Orders: column
    query:
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        revenue: null
        order_count: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 30
```

## Per-series renderers

Use `presentation.series_types` to render review score as a line and delivery days as columns while retaining one shared status axis.

{{< visual id="review_delivery_combo" >}}

```yaml visual-example=review_delivery_combo
visuals:
  review_delivery_combo:
    title: Review and delivery by status
    type: combo
    presentation:
      series_types:
        Review: line
        Delivery days: column
    query:
      dimensions:
        status: orders.status
      metrics:
        review_score: null
        delivery_days: null
      sort:
        - field: status
          direction: asc
```

## Dual axes

Enable `presentation.dual_axis` when the metrics use different scales, then assign line and column marks explicitly with `series_types`.

{{< visual id="revenue_orders_dual_axis_combo" >}}

```yaml visual-example=revenue_orders_dual_axis_combo
visuals:
  revenue_orders_dual_axis_combo:
    title: Revenue and orders dual-axis combo
    type: combo
    presentation:
      dual_axis: true
      series_types:
        Revenue: column
        Orders: line
    query:
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        revenue: null
        order_count: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 60
```
