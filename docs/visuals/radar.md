# Radar chart

Use a radar chart to compare a compact set of category values around a shared scale.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

Radar presentation supports `area` and an optional shared `maximum` across its indicators. Gauge-only fields such as `minimum`, `target`, `showPointer`, `progressWidth`, and `thresholds` do not apply.

## Basic

Use one categorical dimension to create the radar indicators and one metric to set each spoke length. Add a second dimension to compare governed series; the first dimension remains the category and `presentation.legend` controls the series legend.

{{< visual id="status_radar" >}}

```yaml visual-example=status_radar
visuals:
  status_radar:
    title: Order status radar
    description: Compares order status counts on a radar chart.
    type: radar
    presentation:
      type: polar
      area: true
    query:
      type: aggregate
      dimensions:
      - status
      metrics:
      - order_count
      sort:
      - field: order_count
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
      type: aggregate
      dimensions:
      - delivery_bucket
      metrics:
      - order_count
      sort:
      - field: delivery_bucket
        direction: asc
    presentation:
      type: polar
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
      type: polar
      area: false
    query:
      type: aggregate
      dimensions:
      - state
      metrics:
      - revenue
      sort:
      - field: revenue
        direction: desc
      limit: 8
```
