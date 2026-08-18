# Boxplot

Use a boxplot to compare the distribution of a raw metric across categories.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Delivery distribution

Set `query.dataset` and select a numeric metric so LeapView can derive the quartiles, median, whiskers, and outliers from raw delivery values.

{{< visual id="delivery_distribution" >}}

```yaml visual-example=delivery_distribution
visuals:
  delivery_distribution:
    title: Delivery day distribution
    description: Summarizes delivery-day distribution by speed bucket.
    type: boxplot
    query:
      type: distribution
      field: delivery_days
      quantiles:
      - 0.25
      - 0.5
      - 0.75
      outliers: include
      approximation: exact
      whiskers:
        lower: 1.5
        upper: 1.5
    presentation:
      type: cartesian
```

## Review distribution

Swap the numeric metric to compare review-score spread with the same `distribution` shape and raw-table query path.

{{< visual id="review_distribution" >}}

```yaml visual-example=review_distribution
visuals:
  review_distribution:
    title: Review score distribution
    type: boxplot
    query:
      type: distribution
      field: review_score
      quantiles:
      - 0.25
      - 0.5
      - 0.75
      outliers: include
      approximation: exact
      whiskers:
        lower: 1.5
        upper: 1.5
    presentation:
      type: cartesian
```

## Zoomable distribution

Use revenue as the raw metric and enable `presentation.dataZoom` when the range contains values that benefit from closer inspection.

{{< visual id="revenue_distribution" >}}

```yaml visual-example=revenue_distribution
visuals:
  revenue_distribution:
    title: Revenue distribution
    type: boxplot
    presentation:
      type: cartesian
      dataZoom: true
    query:
      type: distribution
      field: revenue
      quantiles:
      - 0.25
      - 0.5
      - 0.75
      outliers: include
      approximation: exact
      whiskers:
        lower: 1.5
        upper: 1.5
```
