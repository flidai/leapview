# Gauge

Use a gauge to communicate one value against a known range or threshold scale.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

Gauge presentation requires an explicit `minimum` and `maximum` domain. Optional `target`, `showPointer`, `progressWidth`, and `thresholds` annotate that domain; gauge values at either configured boundary remain in range.

## Customer review health

Use a naturally bounded score with an explicit target, so the position and distance from goal are immediately interpretable.

{{< visual id="review_gauge" >}}

```yaml visual-example=review_gauge
visuals:
  review_gauge:
    title: Average customer review
    type: gauge
    presentation:
      type: polar
      minimum: 0
      maximum: 5
      target: 4.5
    query:
      type: aggregate
      dimensions: []
      metrics:
      - review_score
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
      type: polar
      minimum: 0
      maximum: 120000
      target: 100000
    query:
      type: aggregate
      dimensions: []
      metrics:
      - order_count
```

## Threshold bands

Add ordered `thresholds` to give score ranges semantic tones;
`progressWidth` controls the arc weight.

{{< visual id="review_gauge_thresholds" >}}

```yaml visual-example=review_gauge_thresholds
visuals:
  review_gauge_thresholds:
    title: Review gauge with thresholds
    type: gauge
    presentation:
      type: polar
      minimum: 0
      maximum: 5
      target: 4.5
      progressWidth: 16
      thresholds:
      - value: 3
        tone: danger
      - value: 4
        tone: warning
      - value: 5
        tone: success
    query:
      type: aggregate
      dimensions: []
      metrics:
      - review_score
```
