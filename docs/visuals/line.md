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
      labels: {density: hidden, priority: [], max_characters: 24, minimum_spacing: 0, tooltip_fallback: true}
    query:
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        revenue: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 30
```

## Multiple series

Map `query.series` to split the metric into one line per order status; the compiler derives the required series-aware Cartesian specification.

{{< visual id="revenue_line_status" >}}

```yaml visual-example=revenue_line_status
visuals:
  revenue_line_status:
    title: Revenue line by status
    type: line
    query:
      dimensions:
        purchase_month: orders.purchase_month
      series:
        field: orders.status
        alias: status
      metrics:
        revenue: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 60
```

## Visual calculation

Use a visual calculation for analysis that belongs only to this result frame, such as a running total. The source and ordering fields are compiler-owned output aliases (`value` and `label` for this line shape), not semantic expressions. LeapView evaluates the closed template on the trusted runtime before the renderer receives the frame.

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
        source: value
        order_by:
          - field: label
            direction: asc
        format: currency
    query:
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        revenue: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 30
```

## Stepped line

Set `presentation.step: true` for discrete changes between periods, hide point symbols for a quieter trace, and enable `data_zoom` for long ranges.

{{< visual id="revenue_line_step" >}}

```yaml visual-example=revenue_line_step
visuals:
  revenue_line_step:
    title: Stepped revenue line
    type: line
    presentation:
      step: true
      show_symbols: false
      data_zoom: true
    query:
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        revenue: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 30
```

## Governed decision context

Use a named context dataset when a title or reference line must be recomputed from the same active semantic filters as the chart. Context queries are compiled with the visual, bounded by the data budget, and delivered in the typed visualization envelope; the renderer cannot issue its own query.

{{< visual id="revenue_line_context" >}}

```yaml visual-example=revenue_line_context
visuals:
  revenue_line_context:
    title: Revenue trend
    subtitle: Current filtered scope
    type: line
    datasets:
      context:
        dimensions:
          status: orders.status
        metrics:
          target:
            metric: revenue
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
      axes:
        - id: x
          title: Month
          tick_density: sparse
        - id: primary_y
          title: Revenue
          scale: linear
          zero: include
          unit: USD
          display_units: thousands
      reference_lines:
        - id: target
          axis: primary_y
          value:
            dataset: context
            field: target
            reducer: mean
          label: Current target
          tone: success
    query:
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        revenue: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 30
```
