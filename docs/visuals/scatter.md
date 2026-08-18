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
      type: cartesian
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
      sort:
      - field: order_id
        direction: asc
      limit: 500
    presentation:
      type: cartesian
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
      type: cartesian
      labels:
        density: automatic
        priority:
        - selected
        - anomaly
        - threshold
        maxCharacters: 16
        minimumSpacing: 6
        tooltipFallback: true
    query:
      type: aggregate
      dimensions:
      - order_id
      metrics:
      - revenue
      - delivery_days
      sort:
      - field: order_id
        direction: asc
      limit: 30
```
