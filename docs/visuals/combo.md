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
      type: cartesian
      labels:
        density: hidden
        priority: []
        maxCharacters: 24
        minimumSpacing: 0
        tooltipFallback: true
    query:
      type: aggregate
      dimensions:
      - purchase_month
      metrics:
      - revenue
      - order_count
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
      type: cartesian
    query:
      type: aggregate
      dimensions:
      - status
      metrics:
      - review_score
      - delivery_days
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
      type: cartesian
    query:
      type: aggregate
      dimensions:
      - purchase_month
      metrics:
      - revenue
      - order_count
      sort:
      - field: purchase_month
        direction: asc
      limit: 60
```
