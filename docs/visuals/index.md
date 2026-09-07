# Visual types

LeapView visuals are defined in dashboard YAML. Every visual has a required `type`, a query, and type-specific presentation settings. Choose the visual that best fits the analytical task; rendering is inferred from its type.

Every preview is compiled and queried from the YAML printed beside it against a fixed documentation dataset. Invalid or stale examples fail documentation generation.

Visual previews are deliberately isolated and do not run the dashboard interaction
runtime. Keep cross-filter and cross-highlight declarations out of these examples.
Use the [Filters and interactions guide](/docs/guides/build/filters-interactions)
and the Visual Showcase interaction matrix to test selections with canonical
filter state, revisions, target planning, and clear behavior.

## Label density and collision policy

Supported built-in ECharts visuals compile `presentation.labels` into one deterministic policy. `hidden` suppresses rendered labels while retaining full tooltip and a bounded accessibility summary; `automatic` shows labels and suppresses collisions; `dense` uses tighter spacing and type for compact matrices; and `always` deliberately disables collision suppression. Policies that may suppress labels require `tooltipFallback: true`; unsupported surfaces such as radar indicators fail compilation instead of silently ignoring the policy.

## Display units

Summary values use `presentation.displayUnits: auto` by default. LeapView selects one shared unit for the complete KPI or numeric-axis scope, renders at most three significant digits, and removes insignificant trailing zeros. The scale never changes independently per tick or mark. Currency, percent, duration, and authored semantic units remain intact; raw values and exact semantic formatting remain available in tooltips, tables, drill detail, exports, and accessibility detail.

Authors can set `displayUnits` to `none`, `thousands`, `millions`, `billions`, or `trillions`. `none` uses the semantic field's canonical unscaled format. A numeric axis can override the visual policy; an omitted axis value inherits `presentation.displayUnits`:

```yaml
presentation:
  displayUnits: auto
  axes:
    - id: primary_y
      scale: linear
      zero: exclude
      displayUnits: millions
      tickDensity: dense
```

Fixed units remain fixed even when the current filtered values are smaller or larger. Use them when comparable visuals must retain the same scale; otherwise prefer `auto`. Label visibility is a separate explicit choice and is never inferred from number formatting.

Axes are renderer-neutral and may also declare `type` (`automatic`, `category`,
`value`, or `time`), `minimum`/`maximum`, `zero`, and `inversion` (`normal` or
`inverted`). Numeric bounds, zero policies, log/linear scales, and number
display units require an effective numeric axis; `dateUnit` is only valid on an
effective time axis. `ticks` and `grid` can be `automatic`, `visible`, or
`hidden`, while `labelRotation` supports `automatic`, `horizontal`, `diagonal`,
and `vertical`. Omitted values resolve to the automatic policy in the compiled
IR, keeping defaults explicit and renderer-neutral. Percent stacking owns the
percent formatter, so it cannot be combined with presentation or primary-axis
`displayUnits`. An explicit `dateUnit` controls axis labels only; governed field
formatting continues to control tooltip values.

For example, this keeps a time axis readable while inverting a bounded value
axis and hiding its grid lines:

```yaml
presentation:
  axes:
  - id: x
    type: time
    dateUnit: month
    ticks: visible
    labelRotation: diagonal
    scale: automatic
    zero: automatic
    tickDensity: normal
  - id: primary_y
    type: value
    scale: linear
    zero: exclude
    minimum: 0
    maximum: 1000000
    inversion: inverted
    grid: hidden
    displayUnits: millions
    tickDensity: dense
```

Policies also bound label length by Unicode grapheme, set minimum collision spacing, and declare whether selected, anomalous, or threshold-crossing data should win a collision. The same frame, locale, dimensions, and policy always produce the same label decision. Full untruncated values remain in governed tooltips when `tooltipFallback` is enabled.

## Curated tooltips and legends

Use the structured `tooltip` item form when a visual needs an explicit tooltip
contract. Items are shown in authored order; each item may override the field
label and semantic format. An explicit empty list suppresses tooltip rows. If
omitted, the renderer preserves the legacy field list and then the visual's
default fields. Values are HTML-escaped and null values display as `—`.

Visible legends on supported Cartesian marks, categorical points, proportional
charts, and radar series accept `legendTitle` and `legendItems`. Candlestick
charts are the exception: they support legend position and title, but not
per-item overrides because the renderer exposes one visual-title series rather
than metric aliases. Listed values on other supported series are filtered to
values present in the current data and rendered in authored order, while
canonical series/category names remain unchanged for selection events.
Unlisted values retain deterministic data order. Geographic reference layers
and hierarchy visuals do not expose legend metadata.

For categorical point legends, `value` addresses the canonical category name:
null and empty categories use `(null)` and `(empty)`, and values whose string
forms collide use the displayed type-qualified name (for example,
`1 [number:1]`).

```yaml
presentation:
  tooltip:
  - field: status
    label: Order status
  - field: revenue
    label: Net revenue
    format:
      kind: currency
      currency: USD
  legend: right
  legendTitle: Segment
  legendItems:
  - value: Consumer
    label: Consumer orders
  - value: Enterprise
```

## Per-mark presentation

Cartesian marks all support the common `labels`, `labelPosition`, `displayUnits`, and `axes` fields. Mark-specific fields are scoped to the renderer paths that consume them:

| Mark | Mark-specific presentation fields |
| --- | --- |
| Line | `legend`, `stacking`, `orientation`, `showSymbols`, `smooth`, `step`, `dataZoom`, `symbolSize`, `seriesIntent`, `referenceLines`, `referenceBands`, `eventAnnotations` |
| Area | `legend`, `stacking`, `orientation`, `showSymbols`, `smooth`, `step`, `dataZoom`, `symbolSize`, `seriesIntent`, `referenceLines`, `referenceBands`, `eventAnnotations` |
| Bar | `legend`, `stacking`, `dataZoom`, `seriesIntent`, `referenceLines`, `referenceBands`, `eventAnnotations` |
| Column | `legend`, `stacking`, `orientation`, `dataZoom`, `seriesIntent`, `referenceLines`, `referenceBands`, `eventAnnotations` |
| Combo | `legend`, `stacking`, `orientation`, `dataZoom`, `series`, `seriesIntent`, `referenceLines`, `referenceBands`, `eventAnnotations`; conditional line controls (`showSymbols`, `smooth`, `step`, `symbolSize`) apply with the default line series or when a configured series is line or area |
| Waterfall | `dataZoom`, `referenceLines`, `referenceBands`, `eventAnnotations` |
| Heatmap | No additional mark-specific fields |
| Histogram | `dataZoom` |
| Candlestick | `legend`, `dataZoom` (legend title is supported; legend item overrides are not) |
| Boxplot | `dataZoom` |

Proportional and polar presentations share the common `legend`, `labels`, and `displayUnits` fields where those channels are meaningful. Mark-specific fields are intentionally scoped to the marks that can render them:

| Mark | Mark-specific presentation fields |
| --- | --- |
| Pie | `rose`, `labelPosition`, `outerRadius` |
| Donut | `rose`, `centerLabel`, `labelPosition`, `innerRadius`, `outerRadius` |
| Funnel | `orientation`, `labelPosition`, `align`, `sort` |
| Radar | `area`, `maximum` |
| Gauge | `minimum`, `maximum`, `target`, `showPointer`, `progressWidth`, `thresholds` |

Gauge has no categorical legend; radar can use `legend` when its aggregate query includes a second governed dimension for series values. A field from another mark's row is rejected during project validation rather than silently changing the rendered visual.

## Decision-context capability matrix

All entries below describe renderer-neutral compiled contracts. Unsupported combinations fail project validation; LeapView never accepts an ECharts option object as a substitute.

| Visuals | Axes | Lines and bands | Events | Conditional formatting | Filtered context datasets and bound metadata |
| --- | --- | --- | --- | --- | --- |
| Line, area, bar, column, combo, scatter, waterfall | Yes | Yes | Yes, on the horizontal axis | Yes | Yes |
| Heatmap | Yes | No | No | Yes | Yes |
| Histogram, candlestick, boxplot | Yes | No | No | No | Yes |
| Pie, donut, funnel | No | No | No | Yes (`mark_fill`, `series_color`) | Yes |
| Treemap, sunburst, tree, Sankey, graph | No | No | No | No | Yes |
| Radar, gauge | No | No | No | No | Yes |
| KPI | No | No | No | Value and background | Yes |
| Table, matrix, pivot | Table-owned sorting and formatting | No | No | Cell foreground/background and icons | Static titles; governed cell bindings |
| Map | Renderer-owned geographic contract | No | No | No | No secondary context datasets |

Conditional-format targets are closed by visual family. Point visuals accept only
`mark_fill`; its field must be one of the rendered `x`, `y`, `size`, `color`,
`label`, or tooltip channels, and the icon cue is rendered as the point symbol.
Supported Cartesian marks retain `mark_fill`, `series_color`, `label_foreground`,
and `icon`. KPI accepts `visual_background` and `kpi_value`, both bound to the
current `value` field, while table, matrix, and pivot accept `cell_foreground`,
`cell_background`, and `icon`. Proportional visuals (`pie`, `donut`, and
`funnel`) currently accept `mark_fill` and `series_color`, both bound to the
`value` field; authored icon cues are rendered in sector labels. Cartesian
conditional formatting is mark-aware across its supported marks.
Row-level mark stroke variation is not part of the rendering contract; use
`mark_fill` or a label/icon cue instead.

Decision-context field references use stable dataset and field identities. Gradient domains, rule order, null/default outcomes, series order, colors, scale domains, zero policies, units, and tick density are explicit in the compiled IR. Bound titles, subtitles, descriptions, summaries, reference values, and accessibility text recompute when filters or data revisions change and use authored fallbacks when governed data is empty.

Deleted fields, unknown datasets, incompatible reducers, unsupported mark/feature combinations, and unsafe formatting intents are deployment errors with the binding path in the diagnostic. Authorization remains part of governed query execution; an unauthorized or failed context query produces the visual’s normal error state and does not reveal a hidden value through metadata or a renderer message.

## Change over time

- [Line chart](/docs/visuals/line)
- [Area chart](/docs/visuals/area)
- [Column chart](/docs/visuals/column)
- [Combo chart](/docs/visuals/combo)
- [Candlestick chart](/docs/visuals/candlestick)

## Compare and rank

- [Bar chart](/docs/visuals/bar)
- [Scatter chart](/docs/visuals/scatter)
- [Funnel chart](/docs/visuals/funnel)
- [Waterfall chart](/docs/visuals/waterfall)
- [Histogram](/docs/visuals/histogram)
- [Boxplot](/docs/visuals/boxplot)
- [Radar chart](/docs/visuals/radar)

## Part-to-whole and hierarchy

- [Pie chart](/docs/visuals/pie)
- [Donut chart](/docs/visuals/donut)
- [Treemap](/docs/visuals/treemap)
- [Tree](/docs/visuals/tree)
- [Sunburst](/docs/visuals/sunburst)

## Relationships and location

- [Heatmap](/docs/visuals/heatmap)
- [Sankey](/docs/visuals/sankey)
- [Graph](/docs/visuals/graph)
- [Map](/docs/visuals/map)

## Summary and exact values

- [Gauge](/docs/visuals/gauge)
- [KPI](/docs/visuals/kpi)
- [Table](/docs/visuals/table)
- [Matrix](/docs/visuals/matrix)
- [Pivot](/docs/visuals/pivot)
