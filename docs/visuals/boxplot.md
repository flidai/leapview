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
      dataset: orders
      dimensions:
        delivery_bucket: orders.delivery_bucket
      metrics:
        delivery_days: null
      sort:
        - field: delivery_bucket
          direction: asc
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
      dataset: orders
      dimensions:
        status: orders.status
      metrics:
        review_score: null
      sort:
        - field: status
          direction: asc
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
      dataset: orders
      dimensions:
        category: orders.category
      metrics:
        revenue: null
      sort:
        - field: category
          direction: desc
      limit: 12
```
