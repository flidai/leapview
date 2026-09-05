# Scatter chart

Use a scatter chart to evaluate the relationship between two quantitative fields. Every point has stable entity identity; optional size, color, series, labels, and tooltips add governed context without changing that identity.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Bivariate scatter

Place delivery time on X and order revenue on Y. Status supplies a categorical legend, while governed conditional formatting controls point fill and symbol and emphasizes canceled orders. Order ID remains the stable point identity.

{{< visual id="delivery_scatter" >}}

```yaml visual-example=delivery_scatter
visuals:
  delivery_scatter:
    title: Delivery time vs order revenue
    type: scatter
    query:
      type: aggregate
      dimensions:
      - order_id
      - status
      metrics:
      - delivery_days
      - revenue
      sort:
      - field: order_id
        direction: asc
      limit: 500
    presentation:
      type: point
      identity: [order_id]
      x: delivery_days
      y: revenue
      color: status
      tooltip: [order_id, status, delivery_days, revenue]
      colorScale:
        kind: categorical
      conditionalFormatting:
      - id: canceled-orders
        target: mark_fill
        field: status
        rule:
          kind: field
          source: status
          values:
            canceled: {color: danger, icon: warning}
          nullStyle: {color: neutral}
          defaultStyle: {color: accent, icon: circle}
      overplot:
        strategy: opacity
        opacity: 0.58
        largeMode: automatic
        largeThreshold: 2000
```

## Bubble chart

Add delivery duration and review score as the two quantitative axes, with category as color. The explicit point identity keeps each order stable.

{{< visual id="delivery_scatter_status" >}}

```yaml visual-example=delivery_scatter_status
visuals:
  delivery_scatter_status:
    title: Review, revenue, and delivery bubble chart
    type: scatter
    query:
      type: aggregate
      dimensions:
      - order_id
      - category
      metrics:
      - delivery_days
      - review_score
      - revenue
      sort:
      - field: order_id
        direction: asc
      limit: 500
    presentation:
      type: point
      identity: [order_id]
      x: review_score
      y: revenue
      size: delivery_days
      color: category
      tooltip: [order_id, category, review_score, revenue, delivery_days]
      colorScale:
        kind: categorical
      sizeScale:
        minimumPixels: 7
        maximumPixels: 34
      overplot:
        strategy: opacity
        opacity: 0.52
```

## Time versus value

Time is a first-class X channel rather than a category label. This deliberately small result labels each stable order and sorts the frame by its governed time alias.

{{< visual id="delivery_scatter_labeled" >}}

```yaml visual-example=delivery_scatter_labeled
visuals:
  delivery_scatter_labeled:
    title: Labeled revenue by purchase time
    type: scatter
    query:
      type: aggregate
      dimensions:
      - order_id
      - dimension: purchase_date
        grain: day
        alias: purchase_day
      metrics:
      - revenue
      sort:
      - field: purchase_day
        direction: asc
      limit: 30
    presentation:
      type: point
      labels:
        density: automatic
        priority:
        - selected
        - anomaly
        - threshold
        maxCharacters: 16
        minimumSpacing: 6
        tooltipFallback: true
      identity: [order_id]
      x: purchase_day
      y: revenue
      label: order_id
      tooltip: [order_id, purchase_day, revenue]
      overplot:
        strategy: show_all
        largeMode: never
```
