# Radar chart

Use a radar chart to compare a compact set of category values around a shared scale.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic

Use one categorical dimension to create the radar indicators and one metric to set each spoke length.

{{< visual id="status_radar" >}}

```yaml visual-example=status_radar
visuals:
  status_radar:
    title: Order status radar
    description: Compares order status counts on a radar chart.
    type: radar
    presentation:
      area: true
    query:
      dimensions:
        status: orders.status
      metrics:
        order_count: null
      sort:
        - field: value
          direction: desc
      limit: 8
```

## Alternate categories

Replace status with delivery buckets to compare a different categorical profile while preserving the same category-value shape.

{{< visual id="delivery_radar" >}}

```yaml visual-example=delivery_radar
visuals:
  delivery_radar:
    title: Delivery speed radar
    type: radar
    query:
      dimensions:
        delivery_bucket: orders.delivery_bucket
      metrics:
        order_count: null
      sort:
        - field: delivery_bucket
          direction: asc
```

## Filled area

Enable `presentation.area` to emphasize the overall revenue profile across states rather than only the outline between individual values.

{{< visual id="state_radar" >}}

```yaml visual-example=state_radar
visuals:
  state_radar:
    title: State revenue radar
    type: radar
    presentation:
      area: false
    query:
      dimensions:
        state: orders.state
      metrics:
        revenue: null
      sort:
        - field: value
          direction: desc
      limit: 8
```
