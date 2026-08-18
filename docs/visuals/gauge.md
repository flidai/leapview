# Gauge

Use a gauge to communicate one value against a known range or threshold scale.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Customer review health

Use a naturally bounded score with an explicit target, so the position and distance from goal are immediately interpretable.

{{< visual id="review_gauge" >}}

```yaml visual-example=review_gauge
visuals:
  review_gauge:
    title: Average customer review
    type: gauge
    presentation:
      minimum: 0
      maximum: 5
      target: 4.5
    query:
      metrics:
        review_score: null
```

## Large-volume domain

Large count domains remain supported when the operating range and target are genuinely meaningful, though a KPI or progress mode is often easier to scan.

{{< visual id="total_orders_gauge" >}}

```yaml visual-example=total_orders_gauge
visuals:
  total_orders_gauge:
    title: Total orders gauge
    type: gauge
    presentation:
      minimum: 0
      maximum: 120000
      target: 100000
    query:
      metrics:
        order_count: null
```

## Threshold bands

Declare `min` and `max`, then add ordered `thresholds` to give score ranges semantic tones; `progress_width` controls the arc weight.

{{< visual id="review_gauge_thresholds" >}}

```yaml visual-example=review_gauge_thresholds
visuals:
  review_gauge_thresholds:
    title: Review gauge with thresholds
    type: gauge
    presentation:
      minimum: 0
      maximum: 5
      target: 4.5
      progress_width: 16
      thresholds:
        - value: 3
          tone: danger
        - value: 4
          tone: warning
        - value: 5
          tone: success
    query:
      metrics:
        review_score: null
```
