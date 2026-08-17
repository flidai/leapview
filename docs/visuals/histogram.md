# Histogram

Use a histogram to show how raw values are distributed across generated numeric bins.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic distribution

Set `query.dataset` and one numeric metric so LeapView can bin raw delivery values and count observations in each interval.

{{< visual id="delivery_histogram" >}}

```yaml visual-example=delivery_histogram
visuals:
  delivery_histogram:
    title: Delivery days histogram
    description: Buckets order volume by delivery duration.
    type: histogram
    presentation:
      histogram_bins: 16
    query:
      dataset: orders
      metrics:
        delivery_days: null
```

## Custom bins

Change the raw metric to revenue and use `presentation.bin_count` to balance distribution detail against the available chart width.

{{< visual id="revenue_histogram" >}}

```yaml visual-example=revenue_histogram
visuals:
  revenue_histogram:
    title: Revenue histogram
    type: histogram
    presentation:
      histogram_bins: 18
    query:
      dataset: orders
      metrics:
        revenue: null
```

## Labeled bins

Use fewer bins for the bounded review scale and an `automatic` label policy so useful bin counts remain visible without uncontrolled overlap.

{{< visual id="review_histogram" >}}

```yaml visual-example=review_histogram
visuals:
  review_histogram:
    title: Review score histogram
    type: histogram
    presentation:
      histogram_bins: 10
      labels: {density: automatic, priority: [selected, anomaly, threshold], max_characters: 12, minimum_spacing: 6, tooltip_fallback: true}
    query:
      dataset: orders
      metrics:
        review_score: null
```
