import "@yacobolo/apigen";
import "../visualization/main.tsp";

@apigen.`package`(#{
  title: "LeapView Dashboard Document",
  version: "1.0.0",
  description: "The canonical renderer-independent Dashboard resource document.",
})
namespace LeapViewDashboard;

/** The only resource version accepted by the canonical dashboard document. */
enum DashboardApiVersion {
  v1: "leapview.dev/v1",
}

/** Common resource kind for a dashboard document. */
enum DashboardResourceKind {
  dashboard: "Dashboard",
}

enum DashboardAppearanceColor {
  slate: "slate",
  gray: "gray",
  red: "red",
  orange: "orange",
  amber: "amber",
  yellow: "yellow",
  lime: "lime",
  green: "green",
  emerald: "emerald",
  teal: "teal",
  cyan: "cyan",
  sky: "sky",
  blue: "blue",
  indigo: "indigo",
  violet: "violet",
  purple: "purple",
  fuchsia: "fuchsia",
  pink: "pink",
  rose: "rose",
}

model DashboardMetadata {
  id: string;
  name: string;
  displayName?: string;
  description?: string;
  owner?: string;
  domain?: string;
  tags?: string[];
  documentation?: string;
}

model DashboardAppearance {
  icon?: string;
  color?: DashboardAppearanceColor;
}

/** The complete canonical Dashboard resource document. */
@apigen.contract(#{ kind: "dashboard-document", tags: #["dashboard", "authoring", "resource"] })
model DashboardDocument {
  apiVersion: DashboardApiVersion;
  kind: DashboardResourceKind;
  metadata: DashboardMetadata;
  spec: DashboardSpec;
}

model DashboardSpec {
  appearance?: DashboardAppearance;
  semanticModel: string;
  layout?: DashboardLayoutDefaults;
  filters: DashboardFilter[];
  visuals: Record<DashboardVisual>;
  pages: DashboardPage[];
  includes?: DashboardIncludes;
}

/** Typed local fragment collections. Includes are resolved before validation. */
model DashboardIncludes {
  visuals?: string[];
  filters?: string[];
  pages?: string[];
  components?: string[];
}

enum DashboardTimeGrain {
  second: "second",
  minute: "minute",
  hour: "hour",
  day: "day",
  week: "week",
  month: "month",
  quarter: "quarter",
  year: "year",
}

/** A semantic dimension reference. Physical dataset fields are not accepted. */
model DashboardDimensionReference {
  dimension: string;
  grain?: DashboardTimeGrain;
  `alias`?: string;
}

/** A semantic metric reference. Metric expressions are never part of this type. */
model DashboardMetricReference {
  metric: string;
  `alias`?: string;
}

/** A physical field reference is only valid for a records query root dataset. */
model DashboardRecordFieldReference {
  field: string;
  `alias`?: string;
}

/** Unaliased selections can use the compact string form; aliased selections use the closed object form. */
union DashboardDimensionSelection {
  name: string,
  reference: DashboardDimensionReference,
}

union DashboardMetricSelection {
  name: string,
  reference: DashboardMetricReference,
}

union DashboardRecordFieldSelection {
  name: string,
  reference: DashboardRecordFieldReference,
}

enum DashboardSortDirection {
  asc: "asc",
  desc: "desc",
}

model DashboardSort {
  /** A compiled result-frame field name, never a source member or expression. */
  field: string;
  direction: DashboardSortDirection;
}

enum DashboardQueryType {
  aggregate: "aggregate",
  records: "records",
  pivot: "pivot",
  histogram: "histogram",
  distribution: "distribution",
}

@discriminator("type")
model DashboardQuery {
  type: string;
}

model AggregateDashboardQuery extends DashboardQuery {
  type: "aggregate";
  dimensions: DashboardDimensionSelection[];
  metrics: DashboardMetricSelection[];
  sort?: DashboardSort[];
  limit?: int32;
}

model RecordsDashboardQuery extends DashboardQuery {
  type: "records";
  dataset: string;
  fields: DashboardRecordFieldSelection[];
  sort?: DashboardSort[];
  limit?: int32;
}

model PivotDashboardQuery extends DashboardQuery {
  type: "pivot";
  rows: DashboardDimensionSelection[];
  columns: DashboardDimensionSelection[];
  metrics: DashboardMetricSelection[];
  sort?: DashboardSort[];
  totals?: DashboardPivotTotals;
  window?: DashboardPivotWindow;
}

model DashboardPivotTotals {
  rows?: boolean;
  columns?: boolean;
  grand?: boolean;
}

model DashboardPivotWindow {
  offset?: int32;
  limit: int32;
}

enum DashboardHistogramNullPolicy {
  omit: "omit",
  include: "include",
}

enum DashboardHistogramApproximation {
  exact: "exact",
  approximate: "approximate",
}

model DashboardHistogramDomain {
  minimum?: float64;
  maximum?: float64;
}

model HistogramDashboardQuery extends DashboardQuery {
  type: "histogram";
  field: DashboardMetricSelection;
  bins: int32;
  domain?: DashboardHistogramDomain;
  nullPolicy: DashboardHistogramNullPolicy;
  approximation: DashboardHistogramApproximation;
}

model DashboardDistributionWhiskers {
  lower: float64;
  upper: float64;
}

enum DashboardDistributionOutlierPolicy {
  omit: "omit",
  include: "include",
}

model DistributionDashboardQuery extends DashboardQuery {
  type: "distribution";
  field: DashboardMetricSelection;
  quantiles: float64[];
  whiskers?: DashboardDistributionWhiskers;
  outliers: DashboardDistributionOutlierPolicy;
  approximation: DashboardHistogramApproximation;
}

enum DashboardFilterOperator {
  isNull: "isNull",
  isNotNull: "isNotNull",
  in: "in",
  notIn: "notIn",
  equals: "equals",
  notEquals: "notEquals",
  contains: "contains",
  notContains: "notContains",
  startsWith: "startsWith",
  endsWith: "endsWith",
  greaterThan: "greaterThan",
  greaterThanOrEqual: "greaterThanOrEqual",
  lessThan: "lessThan",
  lessThanOrEqual: "lessThanOrEqual",
}

enum DashboardFilterValueType {
  string: "string",
  boolean: "boolean",
  integer: "integer",
  decimal: "decimal",
  date: "date",
  timestamp: "timestamp",
}

@discriminator("type")
model DashboardFilterValue {
  type: string;
}

model StringDashboardFilterValue extends DashboardFilterValue {
  type: "string";
  value: string;
}

model BooleanDashboardFilterValue extends DashboardFilterValue {
  type: "boolean";
  value: boolean;
}

model IntegerDashboardFilterValue extends DashboardFilterValue {
  type: "integer";
  /** Precision-safe integer representation. */
  value: string;
}

model DecimalDashboardFilterValue extends DashboardFilterValue {
  type: "decimal";
  /** Canonical decimal representation. */
  value: string;
}

model DateDashboardFilterValue extends DashboardFilterValue {
  type: "date";
  value: plainDate;
}

model TimestampDashboardFilterValue extends DashboardFilterValue {
  type: "timestamp";
  value: utcDateTime;
}

enum DashboardFilterExpressionType {
  unfiltered: "unfiltered",
  nullCheck: "nullCheck",
  set: "set",
  comparison: "comparison",
  range: "range",
  relativePeriod: "relativePeriod",
}

@discriminator("type")
model DashboardFilterExpression {
  type: string;
}

model UnfilteredDashboardFilterExpression extends DashboardFilterExpression {
  type: "unfiltered";
}

model NullCheckDashboardFilterExpression extends DashboardFilterExpression {
  type: "nullCheck";
  operator: DashboardFilterOperator;
}

model SetDashboardFilterExpression extends DashboardFilterExpression {
  type: "set";
  operator: DashboardFilterOperator;
  values: DashboardFilterValue[];
}

model ComparisonDashboardFilterExpression extends DashboardFilterExpression {
  type: "comparison";
  operator: DashboardFilterOperator;
  value: DashboardFilterValue;
}

model DashboardFilterBound {
  value: DashboardFilterValue;
  inclusive: boolean;
}

model RangeDashboardFilterExpression extends DashboardFilterExpression {
  type: "range";
  lower?: DashboardFilterBound;
  upper?: DashboardFilterBound;
}

enum DashboardRelativeDirection {
  previous: "previous",
  current: "current",
  next: "next",
}

enum DashboardRelativeUnit {
  minute: "minute",
  hour: "hour",
  day: "day",
  week: "week",
  month: "month",
  quarter: "quarter",
  year: "year",
}

enum DashboardRelativeAnchor {
  currentTime: "currentTime",
  firstAvailable: "firstAvailable",
  lastAvailable: "lastAvailable",
  fixed: "fixed",
}

model RelativePeriodDashboardFilterExpression extends DashboardFilterExpression {
  type: "relativePeriod";
  direction: DashboardRelativeDirection;
  count: int32;
  unit: DashboardRelativeUnit;
  includeCurrent: boolean;
  anchor: DashboardRelativeAnchor;
  anchorValue?: DashboardFilterValue;
}

enum DashboardFilterControlType {
  singleSelect: "singleSelect",
  multiSelect: "multiSelect",
  text: "text",
  numericRange: "numericRange",
  dateRange: "dateRange",
  relativePeriod: "relativePeriod",
}

@discriminator("type")
model DashboardFilterControl {
  type: string;
}

model SingleSelectDashboardFilterControl extends DashboardFilterControl {
  type: "singleSelect";
  options?: DashboardFilterOptions;
}

model MultiSelectDashboardFilterControl extends DashboardFilterControl {
  type: "multiSelect";
  maxSelectedValues?: int32;
  options?: DashboardFilterOptions;
}

model TextDashboardFilterControl extends DashboardFilterControl {
  type: "text";
}

model NumericRangeDashboardFilterControl extends DashboardFilterControl {
  type: "numericRange";
}

model DateRangeDashboardFilterControl extends DashboardFilterControl {
  type: "dateRange";
}

model RelativePeriodDashboardFilterControl extends DashboardFilterControl {
  type: "relativePeriod";
}

enum DashboardFilterOptionsType {
  distinct: "distinct",
  static: "static",
}

@discriminator("type")
model DashboardFilterOptions {
  type: string;
}

model DistinctDashboardFilterOptions extends DashboardFilterOptions {
  type: "distinct";
  dataset: string;
  dependsOn?: string[];
  limit?: int32;
  includeNull?: boolean;
}

model StaticDashboardFilterOptions extends DashboardFilterOptions {
  type: "static";
  values: DashboardFilterOption[];
}

model DashboardFilterOption {
  value: DashboardFilterValue;
  label: string;
}

model DashboardFilter {
  id: string;
  label: string;
  description?: string;
  dimension: string;
  control: DashboardFilterControl;
  operators?: DashboardFilterOperator[];
  default?: DashboardFilterExpression;
  required?: boolean;
  readerEditable?: boolean;
  targets?: string[];
  urlParameter?: string;
}

enum DashboardLabelDensity {
  hidden: "hidden",
  automatic: "automatic",
  dense: "dense",
  always: "always",
}

enum DashboardLabelPriority {
  selected: "selected",
  anomaly: "anomaly",
  threshold: "threshold",
}

model DashboardLabelPolicy {
  density: DashboardLabelDensity;
  priority?: DashboardLabelPriority[];
  maxCharacters?: int32;
  minimumSpacing?: int32;
  tooltipFallback?: boolean;
}

enum DashboardLegendPosition {
  none: "none",
  top: "top",
  right: "right",
  bottom: "bottom",
  left: "left",
}

enum DashboardStackingMode {
  none: "none",
  normal: "normal",
  percent: "percent",
}

enum DashboardOrientation {
  horizontal: "horizontal",
  vertical: "vertical",
}

/** Renderer-neutral presentation policies; renderer options are intentionally absent. */
@discriminator("type")
model DashboardPresentation {
  type: string;
}

model CartesianDashboardPresentation extends DashboardPresentation {
  type: "cartesian";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  stacking?: DashboardStackingMode;
  orientation?: DashboardOrientation;
  showSymbols?: boolean;
  smooth?: boolean;
  dataZoom?: boolean;
  symbolSize?: float64;
  labelPosition?: DashboardLabelPosition;
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
}

model ProportionalDashboardPresentation extends DashboardPresentation {
  type: "proportional";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
}

model HierarchyDashboardPresentation extends DashboardPresentation {
  type: "hierarchy";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  orientation?: DashboardOrientation;
}

model PolarDashboardPresentation extends DashboardPresentation {
  type: "polar";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
}

model GeographicDashboardPresentation extends DashboardPresentation {
  type: "geographic";
  labels?: DashboardLabelPolicy;
}

model TableDashboardPresentation extends DashboardPresentation {
  type: "table";
  rowHeight: int32;
  showHeader: boolean;
  striped: boolean;
}

model KPIDashboardPresentation extends DashboardPresentation {
  type: "kpi";
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
  note?: string;
  tone?: LeapViewVisualization.VisualizationTone;
}

enum DashboardLabelPosition {
  automatic: "automatic",
  inside: "inside",
  outside: "outside",
  top: "top",
}

model DashboardTextBinding {
  dataset: string;
  field: string;
  reducer?: LeapViewVisualization.VisualizationReferenceReducer;
  prefix?: string;
  suffix?: string;
  fallback?: string;
}

model DashboardMetadataBindings {
  title?: DashboardTextBinding;
  subtitle?: DashboardTextBinding;
  description?: DashboardTextBinding;
  summary?: DashboardTextBinding;
}

enum DashboardVisualType {
  line: "line",
  area: "area",
  bar: "bar",
  column: "column",
  pie: "pie",
  donut: "donut",
  scatter: "scatter",
  funnel: "funnel",
  treemap: "treemap",
  gauge: "gauge",
  heatmap: "heatmap",
  sankey: "sankey",
  graph: "graph",
  map: "map",
  candlestick: "candlestick",
  boxplot: "boxplot",
  combo: "combo",
  waterfall: "waterfall",
  histogram: "histogram",
  radar: "radar",
  tree: "tree",
  sunburst: "sunburst",
  kpi: "kpi",
  table: "table",
  matrix: "matrix",
  pivot: "pivot",
}

model DashboardDataBudget {
  maxRows: int32;
  requiredCompleteness?: LeapViewVisualization.VisualizationCompleteness;
}

model DashboardAccessibility {
  title?: string;
  description?: string;
  summary?: string;
  announceChanges?: boolean;
}

model DashboardCalculation {
  id: string;
  label?: string;
  template: LeapViewVisualization.VisualizationCalculationTemplate;
  source: string;
  axis?: LeapViewVisualization.VisualizationCalculationAxis;
  orderBy?: DashboardSort[];
  partitionBy?: string[];
  reset?: LeapViewVisualization.VisualizationCalculationReset;
  window?: int32;
  offset?: int32;
  parent?: string;
  lookup?: DashboardCalculationLookup;
  hidden?: boolean;
}

model DashboardCalculationLookup {
  field: string;
  value: string;
}

model DashboardVisual {
  type: DashboardVisualType;
  title?: string;
  subtitle?: string;
  description?: string;
  query: DashboardQuery;
  datasets?: Record<DashboardQuery>;
  metadata?: DashboardMetadataBindings;
  presentation: DashboardPresentation;
  accessibility?: DashboardAccessibility;
  dataBudget?: DashboardDataBudget;
  calculations?: DashboardCalculation[];
  interactions?: DashboardInteraction[];
}

enum DashboardInteractionType {
  selection: "selection",
  spatialSelection: "spatialSelection",
}

@discriminator("type")
model DashboardInteraction {
  type: string;
  targets?: string[];
}

enum DashboardSelectionMode {
  single: "single",
  multiple: "multiple",
}

model SelectionDashboardInteraction extends DashboardInteraction {
  type: "selection";
  mode: DashboardSelectionMode;
  toggle: boolean;
  mappings: DashboardInteractionMapping[];
  highlightTargets?: string[];
  noneTargets?: string[];
}

model SpatialSelectionDashboardInteraction extends DashboardInteraction {
  type: "spatialSelection";
  gestures: DashboardSpatialGesture[];
  latitude: DashboardSpatialMapping;
  longitude: DashboardSpatialMapping;
  highlightTargets?: string[];
  noneTargets?: string[];
}

enum DashboardSpatialGesture {
  box: "box",
  lasso: "lasso",
  radius: "radius",
}

model DashboardInteractionMapping {
  field: string;
  dataset?: string;
  grain?: DashboardTimeGrain;
  value: string;
  label?: string;
}

model DashboardSpatialMapping {
  source: string;
  field: string;
  dataset?: string;
}

model DashboardLayoutDefaults {
  columns: int32;
  rowHeight: int32;
  gap: int32;
  padding: int32;
}

model DashboardLayoutOverride {
  columns?: int32;
  rowHeight?: int32;
  gap?: int32;
  padding?: int32;
}

model DashboardPlacement {
  column: int32;
  row: int32;
  columnSpan: int32;
  rowSpan: int32;
}

enum DashboardPageComponentType {
  visual: "visual",
  filter: "filter",
  header: "header",
}

@discriminator("type")
model DashboardPageComponent {
  id: string;
  type: string;
  placement: DashboardPlacement;
}

model VisualDashboardPageComponent extends DashboardPageComponent {
  type: "visual";
  visual: string;
}

model FilterDashboardPageComponent extends DashboardPageComponent {
  type: "filter";
  filter: string;
}

model HeaderDashboardPageComponent extends DashboardPageComponent {
  type: "header";
  title?: string;
  description?: string;
}

model DashboardPage {
  id: string;
  title: string;
  description?: string;
  layout?: DashboardLayoutOverride;
  components: DashboardPageComponent[];
}
