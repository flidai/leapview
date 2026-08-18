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
      type: polar
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
    query:
      type: aggregate
      dimensions: []
      metrics:
      - order_count
```
