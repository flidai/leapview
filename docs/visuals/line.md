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

## Multiple series

Use two ordered `query.dimensions` fields to split the metric into one line per status. The first dimension supplies the category axis and the second supplies the series identity.

{{< visual id="revenue_line_status" >}}

```yaml visual-example=revenue_line_status
visuals:
  revenue_line_status:
    title: Revenue line by status
    type: line
    presentation:
      type: cartesian
    query:
      type: aggregate
      dimensions:
      - purchase_month
      - status
      metrics:
      - revenue
      sort:
      - field: purchase_month
        direction: asc
      limit: 60
```

## Visual calculation

Add a running-total calculation to the same result frame. Calculation references use compiled result aliases, so the ordering field is the category dimension returned by the query.

{{< visual id="revenue_line_running" >}}

```yaml visual-example=revenue_line_running
visuals:
  revenue_line_running:
    title: Revenue and running total
    type: line
    calculations:
    - id: running_revenue
      label: Running revenue
      template: running_total
      source: revenue
      orderBy:
      - field: purchase_month
        direction: asc
    presentation:
      type: cartesian
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

## Stepped line

Set `presentation.step: true` for discrete changes between periods, hide point symbols for a quieter trace, and enable `dataZoom` for long ranges.

{{< visual id="revenue_line_step" >}}

```yaml visual-example=revenue_line_step
visuals:
  revenue_line_step:
    title: Long-range revenue line
    type: line
    presentation:
      type: cartesian
      step: true
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

## Governed decision context

Use a named context dataset when a title or description must be recomputed from the same active semantic filters as the chart. Context queries are compiled with the visual and delivered in the typed visualization envelope.

{{< visual id="revenue_line_context" >}}

```yaml visual-example=revenue_line_context
visuals:
  revenue_line_context:
    title: Revenue trend
    subtitle: Current filtered scope
    type: line
    datasets:
      context:
        type: aggregate
        dimensions:
        - status
        metrics:
        - metric: revenue
          alias: target
        sort:
        - field: status
          direction: asc
        limit: 1
    metadata:
      title:
        dataset: context
        field: status
        reducer: first
        prefix: "Revenue — "
        fallback: Revenue trend
      description:
        dataset: context
        field: target
        reducer: mean
        prefix: "Current target is "
        suffix: " USD."
        fallback: Current target is unavailable.
    presentation:
      type: cartesian
      axes:
      - id: primary_y
        title: Revenue
        scale: linear
        zero: exclude
        displayUnits: millions
        tickDensity: dense
      referenceLines:
      - id: target
        axis: primary_y
        value:
          kind: number
          value: 1000000
        label: Target
        tone: success
      referenceBands:
      - id: observed_range
        axis: primary_y
        from:
          kind: field
          field: revenue
          reducer: minimum
        to:
          kind: field
          field: revenue
          reducer: maximum
        label: Observed range
        tone: neutral
      eventAnnotations:
      - id: fiscal_year
        axis: x
        value:
          kind: text
          value: "2025-01-01"
        label: Fiscal year
        description: Start of fiscal year
        tone: ink
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
