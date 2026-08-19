# KPI

Use a KPI for a governed current value, an optional comparison and delta, an
explicit goal, and a compact historical trend. Comparison, goal, and trend
datasets run through the same semantic model and active filters as the primary
value.

Every preview on this page is generated from the YAML shown below against the
fixed documentation dataset.

Each preview automatically shows every valid responsive arrangement derived
from its explicitly configured features. The YAML does not name layout
variants: LeapView rearranges the same configured fields while enforcing the
minimum size needed to preserve them.

## Decision-ready comparison and trend

Lead with the current result, a filter-aligned baseline, relative change, and
recent history. `favorableDirection` makes the decision meaning explicit.

{{< visual id="revenue_kpi_favorable" >}}

```yaml visual-example=revenue_kpi_favorable
visuals:
  revenue_kpi_favorable:
    title: Revenue versus baseline
    type: kpi
    description: Shows revenue, its filter-aligned baseline, and monthly trend.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    datasets:
      comparison:
        type: aggregate
        dimensions: []
        metrics:
        - revenue_baseline
        limit: 1
      trend:
        type: aggregate
        dimensions:
        - purchase_month
        metrics:
        - revenue
        sort:
        - field: purchase_month
          direction: asc
        limit: 12
    presentation:
      type: kpi
      mode: compact
      comparison:
        dataset: comparison
        field: revenue_baseline
        reducer: first
        label: Baseline
      trend:
        dataset: trend
        category: purchase_month
        value: revenue
      delta: relative
      favorableDirection: increase
      missingComparison: show_unavailable
      displayUnits: auto
```

## Trend only

A trend is an explicit feature, independent of comparison. This example keeps
the current value and historical shape without adding baseline semantics.

{{< visual id="revenue_kpi_trend" >}}

```yaml visual-example=revenue_kpi_trend
visuals:
  revenue_kpi_trend:
    title: Revenue trend
    type: kpi
    description: Shows revenue with an explicitly configured monthly trend.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    datasets:
      trend:
        type: aggregate
        dimensions:
        - purchase_month
        metrics:
        - revenue
        sort:
        - field: purchase_month
          direction: asc
        limit: 12
    presentation:
      type: kpi
      mode: compact
      trend:
        dataset: trend
        category: purchase_month
        value: revenue
```

## Current value

Use compact mode when the current value is meaningful without a comparison. A
note may add context, but it should not duplicate the title.

{{< visual id="total_orders" >}}

```yaml visual-example=total_orders
visuals:
  total_orders:
    title: Total orders
    type: kpi
    description: Shows the filtered count of distinct orders.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - order_count
    presentation:
      type: kpi
      mode: compact
      displayUnits: none
      note: Filtered order count
      tone: ink
```

## Unfavorable direction

The same positive delta is unfavorable when the authored decision context says
decreases are better. The direction and label keep the meaning available
without relying on color.

{{< visual id="revenue_kpi_unfavorable" >}}

```yaml visual-example=revenue_kpi_unfavorable
visuals:
  revenue_kpi_unfavorable:
    title: Cost proxy versus baseline
    type: kpi
    description: Demonstrates an increase that is explicitly unfavorable.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    datasets:
      comparison:
        type: aggregate
        dimensions: []
        metrics:
        - revenue_baseline
        limit: 1
    presentation:
      type: kpi
      comparison:
        dataset: comparison
        field: revenue_baseline
        reducer: first
        label: Baseline
      delta: relative
      favorableDirection: decrease
```

## Bullet with an explicit goal

Bullet and progress modes require a goal binding. Qualitative ranges are
ordered, non-overlapping, and labeled so status never depends on color alone.

{{< visual id="revenue_kpi_bullet" >}}

```yaml visual-example=revenue_kpi_bullet
visuals:
  revenue_kpi_bullet:
    title: Revenue goal
    type: kpi
    description: Shows revenue against a filter-aligned target.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    datasets:
      goal:
        type: aggregate
        dimensions: []
        metrics:
        - revenue_target
        limit: 1
    presentation:
      type: kpi
      mode: bullet
      goal:
        dataset: goal
        field: revenue_target
        reducer: first
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

The progress fill is visually clamped to its track, while the actual value and
explicit operating status remain truthful.

{{< visual id="revenue_kpi_out_of_range" >}}

```yaml visual-example=revenue_kpi_out_of_range
visuals:
  revenue_kpi_out_of_range:
    title: Revenue outside the operating band
    type: kpi
    description: Demonstrates explicit out-of-range status.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    datasets:
      goal:
        type: aggregate
        dimensions: []
        metrics:
        - revenue_target
        limit: 1
    presentation:
      type: kpi
      mode: progress
      goal:
        dataset: goal
        field: revenue_target
        reducer: first
        label: Target
      ranges:
      - maximum: 4000
        label: Operating band
        tone: neutral
```

## Status without a goal

Qualitative status may describe the current value without implying progress
toward a target. The visible label keeps status independent of color.

{{< visual id="revenue_kpi_status" >}}

```yaml visual-example=revenue_kpi_status
visuals:
  revenue_kpi_status:
    title: Revenue operating status
    type: kpi
    description: Shows a current value classified by explicit operating ranges.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    presentation:
      type: kpi
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

This coverage fixture combines subtitle, comparison, progress, goal, status,
trend, and note. Automatic layout may rearrange them, but may not remove any
configured feature.

{{< visual id="revenue_kpi_all_features" >}}

```yaml visual-example=revenue_kpi_all_features
visuals:
  revenue_kpi_all_features:
    title: Revenue decision summary
    subtitle: Current filtered scope
    type: kpi
    description: Exercises every explicit KPI feature in one responsive contract.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    datasets:
      comparison:
        type: aggregate
        dimensions: []
        metrics:
        - revenue_baseline
        limit: 1
      goal:
        type: aggregate
        dimensions: []
        metrics:
        - revenue_target
        limit: 1
      trend:
        type: aggregate
        dimensions:
        - purchase_month
        metrics:
        - revenue
        sort:
        - field: purchase_month
          direction: asc
        limit: 12
    presentation:
      type: kpi
      mode: progress
      comparison:
        dataset: comparison
        field: revenue_baseline
        reducer: first
        label: Baseline
      trend:
        dataset: trend
        category: purchase_month
        value: revenue
      goal:
        dataset: goal
        field: revenue_target
        reducer: first
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
      favorableDirection: increase
      missingComparison: show_unavailable
      note: Filter-aligned decision context
```

## Missing comparison

Choose whether a missing comparison is displayed as unavailable or hidden.
Showing it distinguishes missing context from a zero delta.

{{< visual id="revenue_kpi_missing_comparison" >}}

```yaml visual-example=revenue_kpi_missing_comparison
visuals:
  revenue_kpi_missing_comparison:
    title: Revenue with unavailable comparison
    type: kpi
    description: Demonstrates an explicitly unavailable comparison.
    query:
      type: aggregate
      dimensions: []
      metrics:
      - revenue
    datasets:
      comparison:
        type: aggregate
        dimensions: []
        metrics:
        - missing_revenue
        limit: 1
    presentation:
      type: kpi
      comparison:
        dataset: comparison
        field: missing_revenue
        reducer: first
        label: Prior period
      delta: absolute
      favorableDirection: neutral
      missingComparison: show_unavailable
```
