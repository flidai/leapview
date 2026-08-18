# Visual calculations

Visual calculations are governed post-aggregation analysis attached to one visual. They operate only on fields already present in that visual’s compiled result frame. They do not query the semantic model, traverse model relationships, or accept arbitrary formulas.

## Choose the right calculation layer

Use a semantic metric when the business definition should be reused across visuals, dashboards, APIs, exports, or agents. Revenue, gross margin, active customers, and other governed business concepts belong in the semantic model.

Use a visual calculation when the input is already aggregated and the analysis depends on the displayed structure or ordering. Running totals, moving averages, period differences, percent of parent, percent of grand total, rank, cumulative contribution, and a lookup within the visible frame are good examples.

A visual calculation is stored with one visual. Its result changes with the visual’s governed filters and compiled frame, but it cannot change the meaning of the source metric.

## Author a closed template

```yaml
visuals:
  monthly_revenue:
    type: line
    title: Revenue and running total
    calculations:
      - id: running_revenue
        label: Running revenue
        template: running_total
        source: value
        axis: rows
        order_by:
          - field: label
            direction: asc
        format: currency
    query:
      dimensions:
        month: orders.purchase_month
      metrics:
        revenue: null
      sort:
        - field: month
          direction: asc
      limit: 30
```

`source`, `order_by`, `partition_by`, `parent`, and `lookup.field` address compiled result-frame aliases. They never contain SQL, DAX, JavaScript, or semantic expressions. For built-in categorical charts, the common aliases are `label`, `series`, and `value`. Tables and point visuals retain their explicit query aliases.

The supported templates are:

- `running_total`
- `moving_average`
- `difference`
- `percentage_difference`
- `percent_of_parent`
- `percent_of_grand_total`
- `rank`
- `cumulative_contribution`
- `lookup`

Order-sensitive templates require an explicit `order_by`. `moving_average` also requires a positive `window`. `percent_of_parent` requires `parent` or `partition_by`, and `lookup` requires one unambiguous field/value match within each partition.

Ordering is stable: authored sort fields are applied in sequence, equal values retain result-frame order, and null order values sort last in either direction. Null source values produce null at that row and do not poison later running totals, moving averages, shares, or cumulative contributions.

## Axes, partitions, and reset

`axis` is one of `rows`, `columns`, `hierarchy`, or `facets`. Use `partition_by` to keep independent series, facets, matrix rows, or hierarchy branches from affecting one another.

`reset: highest_parent` uses the first declared hierarchy partition. `reset: lowest_parent` uses all declared hierarchy partitions. `reset: none` keeps the explicit partition unchanged.

Fields used only to order, partition, or look up a result can set `hidden: true`. Hidden fields remain in the governed frame and provenance metadata but are not rendered as a chart series or table column.

## Completeness and provenance

Every compiled field identifies whether it is modeled, aggregated, or visually calculated. Visual-calculation fields retain their calculation ID and source aliases, so tooltips, exports, accessibility projections, and agent results can distinguish them from semantic metrics.

Inline calculations run over the complete bounded result frame. Calculated tables evaluate one deterministic, bounded visible frame before virtualized windows are sliced, so scrolling does not restart a running total. If a query reaches its row cap or otherwise returns an incomplete frame, LeapView marks the visual partial and emits a `visual_calculation_incomplete_frame` diagnostic. It never presents a rank, total, or percentage over a truncated frame as complete.
