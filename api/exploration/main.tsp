import "@yacobolo/apigen";

@apigen.`package`(#{
  title: "LeapView Exploration Specification",
  version: "1.0.0",
  description: "Renderer-neutral authored exploration specifications and durable query state.",
})
namespace LeapViewExploration;

/** Stable semantic model and dataset identifiers. */
@minLength(1)
@maxLength(200)
@pattern("^[A-Za-z0-9][A-Za-z0-9_.:-]*$")
scalar ExplorationResourceID extends string;

/** Semantic member identifiers, never renderer or physical result metadata. */
@minLength(1)
@maxLength(200)
@pattern("^[A-Za-z_][A-Za-z0-9_.:-]*$")
scalar ExplorationFieldID extends string;

/** A selected semantic field or its authored output alias. */
@minLength(1)
@maxLength(200)
@pattern("^[A-Za-z_][A-Za-z0-9_.:-]*$")
scalar ExplorationFieldReference extends string;

/** Stable authored output aliases. */
@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z_][A-Za-z0-9_]*$")
scalar ExplorationAlias extends string;

/** Canonical signed base-10 integer literals; leading zeroes and plus signs are excluded. */
@minLength(1)
@pattern("^(0|-?[1-9][0-9]*)$")
scalar ExplorationIntegerValue extends string;

/** Canonical finite plain decimal literals; exponents and non-zero leading zeroes are excluded. */
@minLength(1)
@pattern("^-?(0|[1-9][0-9]*)(\\.[0-9]+)?$")
scalar ExplorationDecimalValue extends string;

/** Authored visualization formats are kept local so this contract has no
 * dependency on a compiled renderer or dashboard-owned IR package. */
@discriminator("kind")
model ExplorationVisualizationFormat {
  kind: string;
}

model ExplorationNumberVisualizationFormat extends ExplorationVisualizationFormat {
  kind: "number";
  minimumFractionDigits?: int32;
  maximumFractionDigits?: int32;
}

model ExplorationCurrencyVisualizationFormat extends ExplorationVisualizationFormat {
  kind: "currency";
  currency: string;
  minimumFractionDigits?: int32;
  maximumFractionDigits?: int32;
}

model ExplorationPercentVisualizationFormat extends ExplorationVisualizationFormat {
  kind: "percent";
  minimumFractionDigits?: int32;
  maximumFractionDigits?: int32;
}

model ExplorationCompactVisualizationFormat extends ExplorationVisualizationFormat {
  kind: "compact";
  maximumFractionDigits?: int32;
}

model ExplorationDurationVisualizationFormat extends ExplorationVisualizationFormat {
  kind: "duration";
  unit: string;
}

model ExplorationTemporalVisualizationFormat extends ExplorationVisualizationFormat {
  kind: "temporal";
  dateStyle?: string;
  timeStyle?: string;
}

enum ExplorationVisualizationCartesianMark {
  line: "line",
  area: "area",
  bar: "bar",
  column: "column",
  histogram: "histogram",
  combo: "combo",
  waterfall: "waterfall",
  candlestick: "candlestick",
  boxplot: "boxplot",
  heatmap: "heatmap",
}

enum ExplorationVisualizationProportionalMark {
  pie: "pie",
  donut: "donut",
  funnel: "funnel",
}

enum ExplorationVisualizationHierarchyMark {
  treemap: "treemap",
  sunburst: "sunburst",
  tree: "tree",
  sankey: "sankey",
  graph: "graph",
}

enum ExplorationVisualizationPolarMark {
  radar: "radar",
  gauge: "gauge",
}

enum ExplorationVisualizationDisplayUnits {
  auto: "auto",
  none: "none",
  thousands: "thousands",
  millions: "millions",
  billions: "billions",
  trillions: "trillions",
}

enum ExplorationVisualizationKPIMode {
  compact: "compact",
  bullet: "bullet",
  progress: "progress",
}

enum ExplorationVisualizationKPIDeltaMode {
  absolute: "absolute",
  relative: "relative",
}

enum ExplorationVisualizationKPIDirection {
  increase: "increase",
  decrease: "decrease",
  neutral: "neutral",
}

enum ExplorationVisualizationKPIMissingComparison {
  showUnavailable: "show_unavailable",
  hide: "hide",
}

enum ExplorationVisualizationTone {
  neutral: "neutral",
  ink: "ink",
  success: "success",
  warning: "warning",
  danger: "danger",
}

enum ExplorationVisualizationStackingMode {
  none: "none",
  normal: "normal",
  percent: "percent",
}

enum ExplorationVisualizationOrientation {
  horizontal: "horizontal",
  vertical: "vertical",
}

enum ExplorationVisualizationLegendPosition {
  hidden: "hidden",
  top: "top",
  right: "right",
  bottom: "bottom",
  left: "left",
}

model ExplorationVisualizationThreshold {
  value: float64;
  tone: ExplorationVisualizationTone;
}

model ExplorationVisualizationKPIQualitativeRange {
  minimum?: float64;
  maximum?: float64;
  label: string;
  tone: ExplorationVisualizationTone;
}

/** The supported authored calendar grains. */
enum ExplorationTimeGrain {
  second: "second",
  minute: "minute",
  hour: "hour",
  day: "day",
  week: "week",
  month: "month",
  quarter: "quarter",
  year: "year",
}

/** A dimension selection may carry its own time grain and output alias. */
model ExplorationDimensionRef {
  field: ExplorationFieldID;
  `alias`?: ExplorationAlias;
  grain?: ExplorationTimeGrain;
}

/** A metric selection may carry an output alias. */
model ExplorationMetricRef {
  field: ExplorationFieldID;
  `alias`?: ExplorationAlias;
}

enum ExplorationSortDirection {
  asc: "asc",
  desc: "desc",
}

/** Ordered multi-sort over a selected semantic field or authored alias. */
model ExplorationSort {
  field: ExplorationFieldReference;
  direction: ExplorationSortDirection;
}

/** Typed scalar values prevent URL, agent, and execution layers from guessing types. */
@discriminator("kind")
model ExplorationFilterValue {
  kind: string;
}

model StringExplorationFilterValue extends ExplorationFilterValue {
  kind: "string";
  value: string;
}

model BooleanExplorationFilterValue extends ExplorationFilterValue {
  kind: "boolean";
  value: boolean;
}

model IntegerExplorationFilterValue extends ExplorationFilterValue {
  kind: "integer";
  /** Precision-safe integer representation. */
  value: ExplorationIntegerValue;
}

model DecimalExplorationFilterValue extends ExplorationFilterValue {
  kind: "decimal";
  /** Canonical decimal representation. */
  value: ExplorationDecimalValue;
}

model DateExplorationFilterValue extends ExplorationFilterValue {
  kind: "date";
  value: plainDate;
}

model TimestampExplorationFilterValue extends ExplorationFilterValue {
  kind: "timestamp";
  value: utcDateTime;
}

enum ExplorationFilterExpressionKind {
  unfiltered: "unfiltered",
  nullCheck: "null_check",
  set: "set",
  comparison: "comparison",
  range: "range",
  relativePeriod: "relative_period",
}

@discriminator("kind")
model ExplorationFilterExpression {
  kind: string;
}

model UnfilteredExplorationFilterExpression extends ExplorationFilterExpression {
  kind: "unfiltered";
}

model NullCheckExplorationFilterExpression extends ExplorationFilterExpression {
  kind: "null_check";
  operator: "is_null" | "is_not_null";
}

model SetExplorationFilterExpression extends ExplorationFilterExpression {
  kind: "set";
  operator: "in" | "not_in";
  @minItems(1)
  @maxItems(100)
  values: ExplorationFilterValue[];
}

model ComparisonExplorationFilterExpression extends ExplorationFilterExpression {
  kind: "comparison";
  operator: "equals" | "not_equals" | "contains" | "not_contains" | "starts_with" | "ends_with" | "greater_than" | "greater_than_or_equal" | "less_than" | "less_than_or_equal";
  value: ExplorationFilterValue;
}

model ExplorationFilterBound {
  value: ExplorationFilterValue;
  inclusive: boolean;
}

model RangeExplorationFilterExpression extends ExplorationFilterExpression {
  kind: "range";
  lower?: ExplorationFilterBound;
  upper?: ExplorationFilterBound;
}

enum ExplorationRelativeDirection {
  previous: "previous",
  current: "current",
  next: "next",
}

enum ExplorationRelativeUnit {
  minute: "minute",
  hour: "hour",
  day: "day",
  week: "week",
  month: "month",
  quarter: "quarter",
  year: "year",
}

enum ExplorationRelativeAnchor {
  currentTime: "current_time",
  firstAvailable: "first_available",
  lastAvailable: "last_available",
  fixed: "fixed",
}

model RelativePeriodExplorationFilterExpression extends ExplorationFilterExpression {
  kind: "relative_period";
  direction: ExplorationRelativeDirection;
  @minValue(1)
  count: int32;
  unit: ExplorationRelativeUnit;
  includeCurrent: boolean;
  anchor: ExplorationRelativeAnchor;
  anchorValue?: ExplorationFilterValue;
}

/** A typed semantic predicate, optionally scoped to a participating dataset. */
model ExplorationFilter {
  field: ExplorationFieldID;
  datasetId?: ExplorationResourceID;
  expression: ExplorationFilterExpression;
}

@discriminator("kind")
model ExplorationTemporalValue {
  kind: string;
}

model DateExplorationTemporalValue extends ExplorationTemporalValue {
  kind: "date";
  value: plainDate;
}

model TimestampExplorationTemporalValue extends ExplorationTemporalValue {
  kind: "timestamp";
  value: utcDateTime;
}

model ExplorationTimeBound {
  value: ExplorationTemporalValue;
  inclusive: boolean;
}

@discriminator("kind")
model ExplorationTimeRange {
  kind: string;
}

/** Absolute bounds may be open on either side, but at least one is required by validation. */
model AbsoluteExplorationTimeRange extends ExplorationTimeRange {
  kind: "absolute";
  lower?: ExplorationTimeBound;
  upper?: ExplorationTimeBound;
}

model RelativeExplorationTimeRange extends ExplorationTimeRange {
  kind: "relative";
  direction: ExplorationRelativeDirection;
  @minValue(1)
  count: int32;
  unit: ExplorationRelativeUnit;
  includeCurrent: boolean;
  anchor: ExplorationRelativeAnchor;
  anchorValue?: ExplorationTemporalValue;
}

model ExplorationTimeSelection {
  field: ExplorationFieldID;
  grain: ExplorationTimeGrain;
  `alias`?: ExplorationAlias;
  range?: ExplorationTimeRange;
}

@minValue(1)
@maxValue(1000)
scalar ExplorationRowLimit extends int32;

model ExplorationPivotTotals {
  rows?: boolean;
  columns?: boolean;
  grand?: boolean;
}

model ExplorationPivotWindow {
  @minValue(0)
  offset?: int32;
  limit: ExplorationRowLimit;
}

/** Pivot query shape authored independently of renderer output state. */
model ExplorationPivotConfig {
  @maxItems(100)
  rows: ExplorationDimensionRef[];
  @maxItems(100)
  columns: ExplorationDimensionRef[];
  @maxItems(100)
  metrics: ExplorationMetricRef[];
  @maxItems(100)
  sort?: ExplorationSort[];
  totals?: ExplorationPivotTotals;
  window?: ExplorationPivotWindow;
}

model ExplorationTableColumn {
  field: ExplorationFieldReference;
  label?: string;
  @minValue(1)
  @maxValue(2000)
  width?: int32;
  format?: ExplorationVisualizationFormat;
}

enum ExplorationTableDensity {
  compact: "compact",
  comfortable: "comfortable",
}

/** Durable table preferences; rows, loading state, and result data are excluded. */
model ExplorationTableDisplayConfig {
  @maxItems(100)
  columns?: ExplorationTableColumn[];
  density?: ExplorationTableDensity;
  striped?: boolean;
  showHeader?: boolean;
  @minValue(1)
  @maxValue(2000)
  rowHeight?: int32;
}

/** Authored visualization bindings use semantic IDs or authored aliases, never result data. */
model ExplorationVisualizationFieldRef {
  field: ExplorationFieldReference;
  format?: ExplorationVisualizationFormat;
}

@discriminator("kind")
model ExplorationVisualizationConfig {
  kind: string;
  title?: string;
  subtitle?: string;
  legend?: ExplorationVisualizationLegendPosition;
  displayUnits?: ExplorationVisualizationDisplayUnits;
  orientation?: ExplorationVisualizationOrientation;
  stacking?: ExplorationVisualizationStackingMode;
}

model CartesianExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "cartesian";
  mark: ExplorationVisualizationCartesianMark;
  x?: ExplorationVisualizationFieldRef;
  y?: ExplorationVisualizationFieldRef[];
  series?: ExplorationVisualizationFieldRef;
  smooth?: boolean;
  showSymbols?: boolean;
}

model PointExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "point";
  mark: "point";
  x: ExplorationVisualizationFieldRef;
  y: ExplorationVisualizationFieldRef;
  size?: ExplorationVisualizationFieldRef;
  color?: ExplorationVisualizationFieldRef;
  identity?: ExplorationVisualizationFieldRef[];
}

model ProportionalExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "proportional";
  mark: ExplorationVisualizationProportionalMark;
  category: ExplorationVisualizationFieldRef;
  value: ExplorationVisualizationFieldRef;
  series?: ExplorationVisualizationFieldRef;
}

model HierarchyExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "hierarchy";
  mark: ExplorationVisualizationHierarchyMark;
  node: ExplorationVisualizationFieldRef;
  parent?: ExplorationVisualizationFieldRef;
  value?: ExplorationVisualizationFieldRef;
}

model PolarExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "polar";
  mark: ExplorationVisualizationPolarMark;
  category?: ExplorationVisualizationFieldRef;
  value: ExplorationVisualizationFieldRef;
  series?: ExplorationVisualizationFieldRef;
}

enum ExplorationGeographicLayerKind {
  choropleth: "choropleth",
  point: "point",
  heat: "heat",
  density: "density",
  reference: "reference",
  path: "path",
}

model TableExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "table";
  @maxItems(100)
  columns: ExplorationVisualizationFieldRef[];
}

model MatrixExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "matrix";
  @maxItems(100)
  rows: ExplorationVisualizationFieldRef[];
  @maxItems(100)
  columns: ExplorationVisualizationFieldRef[];
  @maxItems(100)
  metrics: ExplorationVisualizationFieldRef[];
}

model PivotExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "pivot";
  @maxItems(100)
  rows: ExplorationVisualizationFieldRef[];
  @maxItems(100)
  columns: ExplorationVisualizationFieldRef[];
  @maxItems(100)
  metrics: ExplorationVisualizationFieldRef[];
}

model ExplorationKPITrend {
  category: ExplorationVisualizationFieldRef;
  value: ExplorationVisualizationFieldRef;
}

/** Optional authored KPI presentation; omitted properties retain renderer defaults. */
model ExplorationKPIPresentation {
  mode?: ExplorationVisualizationKPIMode;
  delta?: ExplorationVisualizationKPIDeltaMode;
  favorableDirection?: ExplorationVisualizationKPIDirection;
  missingComparison?: ExplorationVisualizationKPIMissingComparison;
  ranges?: ExplorationVisualizationKPIQualitativeRange[];
  displayUnits?: ExplorationVisualizationDisplayUnits;
  note?: string;
  tone?: ExplorationVisualizationTone;
  thresholds?: ExplorationVisualizationThreshold[];
}

model KPIExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "kpi";
  value: ExplorationVisualizationFieldRef;
  comparison?: ExplorationVisualizationFieldRef;
  goal?: ExplorationVisualizationFieldRef;
  trend?: ExplorationKPITrend;
  presentation?: ExplorationKPIPresentation;
}

model GeographicExplorationVisualization extends ExplorationVisualizationConfig {
  kind: "geographic";
  layer: ExplorationGeographicLayerKind;
  latitude: ExplorationVisualizationFieldRef;
  longitude: ExplorationVisualizationFieldRef;
  color?: ExplorationVisualizationFieldRef;
  size?: ExplorationVisualizationFieldRef;
}

/**
 * Complete authored exploration state. This is deliberately not a
 * VisualizationSpec or VisualizationEnvelope: it contains no compiled field
 * schema, renderer ID, data revision/state, selection, rows, or diagnostics.
 */
@apigen.contract(#{ kind: "exploration-spec", tags: #["exploration", "authoring", "resource"] })
model ExplorationSpec {
  schemaVersion: 1;
  modelId: ExplorationResourceID;
  datasetId?: ExplorationResourceID;
  @maxItems(100)
  dimensions: ExplorationDimensionRef[];
  @maxItems(100)
  metrics: ExplorationMetricRef[];
  @maxItems(100)
  filters: ExplorationFilter[];
  time?: ExplorationTimeSelection;
  @maxItems(100)
  sort: ExplorationSort[];
  limit: ExplorationRowLimit;
  pivot?: ExplorationPivotConfig;
  table?: ExplorationTableDisplayConfig;
  visualization?: ExplorationVisualizationConfig;
}
