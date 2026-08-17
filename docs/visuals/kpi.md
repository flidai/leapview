# KPI

Use a KPI for a governed current value, an optional comparison and delta, an explicit goal, and a compact historical trend. Comparison, goal, and trend datasets run through the same semantic model and active filters as the primary value.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

Each preview automatically shows every valid responsive arrangement derived from that visual's explicitly configured features. The YAML does not name layout variants: LeapView rearranges the same label, value, comparison, goal, status, trend, and note, while enforcing the minimum size needed to preserve them.

## Decision-ready comparison and trend

Lead with the current result, a filter-aligned baseline, the relative change, and recent history. `favorable_direction` makes the decision meaning explicit instead of assuming that an increase is good.

{{< visual id="revenue_kpi_favorable" >}}

```yaml visual-example=revenue_kpi_favorable
visuals:
  revenue_kpi_favorable:
    title: Revenue versus baseline
    type: kpi
    description: Shows revenue, its filter-aligned baseline, and monthly trend.
    query:
      metrics:
        revenue: null
    datasets:
      comparison:
        metrics:
          value:
            metric: revenue_baseline
        limit: 1
      trend:
        dimensions:
          period: orders.purchase_month
        metrics:
          value:
            metric: revenue
        sort:
          - field: period
            direction: asc
        limit: 12
    kpi:
      mode: compact
      comparison:
        dataset: comparison
        field: value
        reducer: first
        label: Baseline
      trend:
        dataset: trend
        category: period
        value: value
      delta: relative
      favorable_direction: increase
      missing_comparison: show_unavailable
    presentation:
      display_units: auto
```

## Trend only

A trend is an explicit feature, independent of comparison. This example keeps the current value and historical shape without adding baseline semantics.

{{< visual id="revenue_kpi_trend" >}}

```yaml visual-example=revenue_kpi_trend
visuals:
  revenue_kpi_trend:
    title: Revenue trend
    type: kpi
    description: Shows revenue with an explicitly configured monthly trend.
    query:
      metrics:
        revenue: null
    datasets:
      trend:
        dimensions:
          period: orders.purchase_month
        metrics:
          value:
            metric: revenue
        sort:
          - field: period
            direction: asc
        limit: 12
    kpi:
      mode: compact
      trend:
        dataset: trend
        category: period
        value: value
```

## Current value

Use compact mode when the current value is meaningful without a comparison. A note may add context, but it should not duplicate the title.

{{< visual id="total_orders" >}}

```yaml visual-example=total_orders
visuals:
  total_orders:
    type: kpi
    description: Shows the filtered count of distinct orders.
    query:
      metrics:
        order_count: null
    kpi:
      mode: compact
    presentation:
      display_units: none
      note: Filtered order count
      tone: ink
```

## Unfavorable direction

The same positive delta is unfavorable when the authored decision context says decreases are better. The arrow, value, and status word keep the meaning available without color.

{{< visual id="revenue_kpi_unfavorable" >}}

```yaml visual-example=revenue_kpi_unfavorable
visuals:
  revenue_kpi_unfavorable:
    title: Cost proxy versus baseline
    type: kpi
    description: Demonstrates an increase that is explicitly unfavorable.
    query:
      metrics:
        revenue: null
    datasets:
      comparison:
        metrics:
          value:
            metric: revenue_baseline
        limit: 1
    kpi:
      comparison:
        dataset: comparison
        field: value
        label: Baseline
      delta: relative
      favorable_direction: decrease
```

## Bullet with an explicit goal

Bullet and progress modes require a goal binding. Qualitative ranges are ordered, non-overlapping, and labeled so status never depends on color alone.

{{< visual id="revenue_kpi_bullet" >}}

```yaml visual-example=revenue_kpi_bullet
visuals:
  revenue_kpi_bullet:
    title: Revenue goal
    type: kpi
    description: Shows revenue against a filter-aligned target.
    query:
      metrics:
        revenue: null
    datasets:
      goal:
        metrics:
          value:
            metric: revenue_target
        limit: 1
    kpi:
      mode: bullet
      goal:
        dataset: goal
        field: value
        label: Target
      ranges:
        - maximum: 4000
          label: Behind
          tone: danger
        - minimum: 4000
          maximum: 5000
          label: On track
          tone: success
        - minimum: 5000
          label: Ahead
          tone: ink
```

## Progress with an out-of-range value

The progress fill is visually clamped to its track, while the actual value and the explicit “Out of range” status remain truthful.

{{< visual id="revenue_kpi_out_of_range" >}}

```yaml visual-example=revenue_kpi_out_of_range
visuals:
  revenue_kpi_out_of_range:
    title: Revenue outside the operating band
    type: kpi
    description: Demonstrates explicit out-of-range status.
    query:
      metrics:
        revenue: null
    datasets:
      goal:
        metrics:
          value:
            metric: revenue_target
        limit: 1
    kpi:
      mode: progress
      goal:
        dataset: goal
        field: value
        label: Target
      ranges:
        - maximum: 4000
          label: Operating band
          tone: neutral
```

## Status without a goal

Qualitative status may describe the current value without implying progress toward a target. The visible label keeps the status independent of color.

{{< visual id="revenue_kpi_status" >}}

```yaml visual-example=revenue_kpi_status
visuals:
  revenue_kpi_status:
    title: Revenue operating status
    type: kpi
    description: Shows a current value classified by explicit operating ranges.
    query:
      metrics:
        revenue: null
    kpi:
      mode: compact
      ranges:
        - maximum: 4000
          label: Below plan
          tone: warning
        - minimum: 4000
          maximum: 6000
          label: On plan
          tone: success
        - minimum: 6000
          label: Above plan
          tone: ink
```

## All explicit features

This coverage fixture intentionally combines subtitle, comparison, progress, goal, status, trend, and note. Automatic layout may rearrange them, but it may not remove or reformat any configured feature.

{{< visual id="revenue_kpi_all_features" >}}

```yaml visual-example=revenue_kpi_all_features
visuals:
  revenue_kpi_all_features:
    title: Revenue decision summary
    subtitle: Current filtered scope
    type: kpi
    description: Exercises every explicit KPI feature in one responsive contract.
    query:
      metrics:
        revenue: null
    datasets:
      comparison:
        metrics:
          value:
            metric: revenue_baseline
        limit: 1
      goal:
        metrics:
          value:
            metric: revenue_target
        limit: 1
      trend:
        dimensions:
          period: orders.purchase_month
        metrics:
          value:
            metric: revenue
        sort:
          - field: period
            direction: asc
        limit: 12
    kpi:
      mode: progress
      comparison:
        dataset: comparison
        field: value
        label: Baseline
      trend:
        dataset: trend
        category: period
        value: value
      goal:
        dataset: goal
        field: value
        label: Target
      ranges:
        - maximum: 4000
          label: Behind
          tone: danger
        - minimum: 4000
          maximum: 6000
          label: On track
          tone: success
        - minimum: 6000
          label: Ahead
          tone: ink
      delta: relative
      favorable_direction: increase
      missing_comparison: show_unavailable
    presentation:
      note: Filter-aligned decision context
```

## Missing comparison

Choose whether a missing comparison is displayed as unavailable or hidden. Showing it is the safer default because it distinguishes missing context from a zero delta.

{{< visual id="revenue_kpi_missing_comparison" >}}

```yaml visual-example=revenue_kpi_missing_comparison
visuals:
  revenue_kpi_missing_comparison:
    title: Revenue with unavailable comparison
    type: kpi
    description: Demonstrates an explicitly unavailable comparison.
    query:
      metrics:
        revenue: null
    datasets:
      comparison:
        metrics:
          value:
            metric: missing_revenue
        limit: 1
    kpi:
      comparison:
        dataset: comparison
        field: value
        label: Prior period
      delta: absolute
      favorable_direction: neutral
      missing_comparison: show_unavailable
```
