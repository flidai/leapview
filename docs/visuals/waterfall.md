# Waterfall chart

Use a waterfall chart to show how category contributions build from one total to another.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Revenue bridge

Use signed business contributions so the bridge visibly distinguishes gains from losses and explains the net movement.

{{< visual id="revenue_bridge_waterfall" >}}

```yaml visual-example=revenue_bridge_waterfall
visuals:
  revenue_bridge_waterfall:
    title: Revenue drivers
    description: Explains positive and negative contributions to net revenue growth.
    type: waterfall
    presentation:
      type: cartesian
      labels:
        density: automatic
        priority:
        - selected
        - anomaly
        - threshold
        maxCharacters: 18
        minimumSpacing: 6
        tooltipFallback: true
    query:
      type: aggregate
      dimensions:
      - component
      metrics:
      - revenue_impact
      sort:
      - field: component
        direction: asc
```

## Alternate metric

Replace revenue with order count to reuse the same running-contribution structure for volume rather than value.

{{< visual id="orders_waterfall" >}}

```yaml visual-example=orders_waterfall
visuals:
  orders_waterfall:
    title: Monthly order contribution
    type: waterfall
    query:
      type: aggregate
      dimensions:
      - purchase_month
      metrics:
      - order_count
      sort:
      - field: purchase_month
        direction: asc
      limit: 18
    presentation:
      type: cartesian
```

## Labels and zoom

Use `presentation.labels` for exact contributions and `dataZoom` when many categories make the running sequence dense. Automatic collision handling keeps compact cards readable.

{{< visual id="revenue_waterfall_labeled" >}}

```yaml visual-example=revenue_waterfall_labeled
visuals:
  revenue_waterfall_labeled:
    title: Labeled revenue waterfall
    type: waterfall
    presentation:
      type: cartesian
      labels:
        density: automatic
        priority:
        - selected
        - anomaly
        - threshold
        maxCharacters: 18
        minimumSpacing: 6
        tooltipFallback: true
      dataZoom: true
    query:
      type: aggregate
      dimensions:
      - category
      metrics:
      - revenue
      sort:
      - field: value
        direction: desc
      limit: 12
```
