# Boxplot

Use a boxplot to compare quantiles and outliers for a governed numeric field.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Delivery distribution

Select a numeric field, a semantic grouping dimension, and explicit quantiles
so LeapView can derive comparable quartiles, medians, whiskers, and outliers.

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
      group: delivery_bucket
      quantiles:
      - 0.25
      - 0.5
      - 0.75
      outliers: omit
      approximation: exact
      whiskers:
        lower: 0.05
        upper: 0.95
    presentation:
      type: cartesian
```

## Review distribution

Swap the numeric field to compare review-score spread with the same `distribution` shape.

{{< visual id="review_distribution" >}}

```yaml visual-example=review_distribution
visuals:
  review_distribution:
    title: Review score distribution
    type: boxplot
    query:
      type: distribution
      field: review_score
      group: status
      quantiles:
      - 0.25
      - 0.5
      - 0.75
      outliers: omit
      approximation: exact
      whiskers:
        lower: 0.05
        upper: 0.95
    presentation:
      type: cartesian
```

## Zoomable distribution

Use revenue as the numeric field and enable `presentation.dataZoom` when the range contains values that benefit from closer inspection.

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
      group: category
      quantiles:
      - 0.25
      - 0.5
      - 0.75
      outliers: omit
      approximation: exact
      whiskers:
        lower: 0.05
        upper: 0.95
      limit: 12
```
