# Histogram

Use a histogram to show how raw values are distributed across generated numeric bins.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic distribution

Select one numeric field and a bin count so LeapView can count observations in each interval.

{{< visual id="delivery_histogram" >}}

```yaml visual-example=delivery_histogram
visuals:
  delivery_histogram:
    title: Delivery days histogram
    description: Buckets order volume by delivery duration.
    type: histogram
    presentation:
      type: cartesian
    query:
      type: histogram
      field: delivery_days
      bins: 16
      nullPolicy: omit
      approximation: exact
```

## Custom bins

Change the numeric field to revenue and adjust `query.bins` to balance distribution detail against the available chart width.

{{< visual id="revenue_histogram" >}}

```yaml visual-example=revenue_histogram
visuals:
  revenue_histogram:
    title: Revenue histogram
    type: histogram
    presentation:
      type: cartesian
    query:
      type: histogram
      field: revenue
      bins: 18
      nullPolicy: omit
      approximation: exact
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
      type: cartesian
      labels:
        density: automatic
        priority:
        - selected
        - anomaly
        - threshold
        maxCharacters: 12
        minimumSpacing: 6
        tooltipFallback: true
    query:
      type: histogram
      field: review_score
      bins: 10
      nullPolicy: omit
      approximation: exact
```
