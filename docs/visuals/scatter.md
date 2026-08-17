# Scatter chart

Use a scatter chart to evaluate the relationship between two quantitative fields. Every point has stable entity identity; optional size, color, series, labels, and tooltips add governed context without changing that identity.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Bivariate scatter

Place delivery time on X and order revenue on Y. Status supplies a categorical color channel, while order ID remains the stable point identity.

{{< visual id="delivery_scatter" >}}

```yaml visual-example=delivery_scatter
visuals:
  delivery_scatter:
    title: Delivery time vs order revenue
    type: scatter
    query:
      dimensions:
        order_id: orders.order_id
        status: orders.status
      metrics:
        delivery_days: null
        revenue: null
      sort:
        - field: order_id
          direction: asc
      limit: 500
    point:
      identity: [order_id]
      x: delivery_days
      y: revenue
      color: status
      tooltip: [order_id, status, delivery_days, revenue]
      color_scale: {kind: categorical}
      overplot: {strategy: opacity, opacity: 0.58, large_mode: automatic, large_threshold: 2000}
```

## Bubble chart

Add delivery duration as bubble size and category as color. The explicit pixel range keeps bubble area legible without allowing one outlier to cover the plot.

{{< visual id="delivery_scatter_status" >}}

```yaml visual-example=delivery_scatter_status
visuals:
  delivery_scatter_status:
    title: Review, revenue, and delivery bubble chart
    type: scatter
    query:
      dimensions:
        order_id: orders.order_id
        category: orders.category
      metrics:
        delivery_days: null
        review_score: null
        revenue: null
      sort:
        - field: order_id
          direction: asc
      limit: 500
    point:
      identity: [order_id]
      x: review_score
      y: revenue
      size: delivery_days
      color: category
      tooltip: [order_id, category, review_score, revenue, delivery_days]
      color_scale: {kind: categorical}
      size_scale: {minimum_pixels: 7, maximum_pixels: 34}
      overplot: {strategy: opacity, opacity: 0.52}
```

## Time versus value

Time is a first-class X channel rather than a category label. This deliberately small result labels each stable order and sorts the frame by its governed time alias.

{{< visual id="delivery_scatter_labeled" >}}

```yaml visual-example=delivery_scatter_labeled
visuals:
  delivery_scatter_labeled:
    title: Labeled revenue by purchase time
    type: scatter
    presentation:
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 16, minimum_spacing: 6, tooltip_fallback: true}
    query:
      dimensions:
        order_id: orders.order_id
      time:
        field: purchase_date
        grain: day
        alias: purchase_day
      metrics:
        revenue: null
      sort:
        - field: purchase_day
          direction: asc
      limit: 30
    point:
      identity: [order_id]
      x: purchase_day
      y: revenue
      label: order_id
      tooltip: [order_id, purchase_day, revenue]
      overplot: {strategy: show_all, large_mode: never}
```
