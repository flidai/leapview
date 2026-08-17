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
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 18, minimum_spacing: 6, tooltip_fallback: true}
    query:
      table: revenue_bridge
      dimensions:
        component: revenue_bridge.component
      metrics:
        revenue_impact: null
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
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        order_count: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 18
```

## Labels and zoom

Use `presentation.labels` for exact contributions and `data_zoom` when many categories make the running sequence dense. Automatic collision handling keeps compact cards readable.

{{< visual id="revenue_waterfall_labeled" >}}

```yaml visual-example=revenue_waterfall_labeled
visuals:
  revenue_waterfall_labeled:
    title: Labeled revenue waterfall
    type: waterfall
    presentation:
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 18, minimum_spacing: 6, tooltip_fallback: true}
      data_zoom: true
    query:
      dimensions:
        category: orders.category
      metrics:
        revenue: null
      sort:
        - field: value
          direction: desc
      limit: 12
```
