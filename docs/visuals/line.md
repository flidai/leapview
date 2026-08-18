# Line chart

Use a line chart to show a metric changing across an ordered category such as time.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Basic

Use one ordered `query.dimensions` field for the horizontal axis and one `query.metrics` field for the plotted value. Sorting by month keeps the line chronological, while explicitly hidden labels keep the trend readable.

{{< visual id="revenue_line" >}}

```yaml visual-example=revenue_line
visuals:
  revenue_line:
    title: Revenue line by month
    type: line
    presentation:
      type: cartesian
      labels:
        density: hidden
        priority: []
        maxCharacters: 24
        minimumSpacing: 0
        tooltipFallback: true
    query:
      type: aggregate
      dimensions:
      - purchase_month
      metrics:
      - revenue
      sort:
      - field: purchase_month
        direction: asc
      limit: 30
```

## Long-range line

Hide point symbols for a quieter trace and enable `dataZoom` for long ranges.

{{< visual id="revenue_line_step" >}}

```yaml visual-example=revenue_line_step
visuals:
  revenue_line_step:
    title: Long-range revenue line
    type: line
    presentation:
      type: cartesian
      showSymbols: false
      dataZoom: true
    query:
      type: aggregate
      dimensions:
      - purchase_month
      metrics:
      - revenue
      sort:
      - field: purchase_month
        direction: asc
      limit: 30
```
