import "@yacobolo/apigen";
import "../visualization/main.tsp";

@apigen.`package`(#{
  title: "LeapView Dashboard Document",
  version: "1.0.0",
  description: "The canonical renderer-independent Dashboard resource document.",
})
namespace LeapViewDashboard;

/** Graph resource identity used by dashboard metadata and semantic-model links. */
@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z0-9][A-Za-z0-9_.:-]*$")
scalar DashboardResourceID extends string;

/** Human-readable resource name; unlike graph IDs, names cannot begin with a digit. */
@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z_][A-Za-z0-9_.-]*$")
scalar DashboardResourceName extends string;

/** Stable local identity for dashboard-owned objects and map keys. */
@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z_][A-Za-z0-9_-]*$")
scalar DashboardLocalIdentifier extends string;

/** Compiled result-frame names used by downstream bindings and sorts. */
@minLength(1)
@pattern("^[A-Za-z_][A-Za-z0-9_]*$")
scalar DashboardResultField extends string;

/** Physical records field on the query's single root dataset. */
@minLength(1)
@pattern("^[A-Za-z_][A-Za-z0-9_]*$")
scalar DashboardRecordField extends string;

/** Friendly URL state parameter (protocol-reserved names remain contextual rules). */
@minLength(1)
@maxLength(64)
@pattern("^[A-Za-z][A-Za-z0-9_-]{0,63}$")
scalar DashboardURLParameter extends string;

/** Semantic member references are unqualified governed identifiers. */
scalar DashboardSemanticMember extends DashboardResultField;

/** Dashboard visual, filter, page, component, calculation, and dataset IDs. */
scalar DashboardObjectIdentifier extends DashboardLocalIdentifier;

/** Visual definition ID or report-qualified page/component target. */
@minLength(1)
@maxLength(257)
@pattern("^[A-Za-z_][A-Za-z0-9_-]*(?:/[A-Za-z_][A-Za-z0-9_-]*)?$")
scalar DashboardFilterTarget extends string;

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
  id: DashboardResourceID;
  name: DashboardResourceName;
  displayName?: string;
  description?: string;
  owner?: string;
  domain?: string;
  tags?: string[];
  documentation?: string;
  /** Organization-curated discovery signal used to feature this dashboard. */
  featured?: boolean;
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
  semanticModel: DashboardResourceID;
  layout?: DashboardLayoutDefaults;
  filters: DashboardFilter[];
  @apigen.propertyNames(DashboardObjectIdentifier)
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
  dimension: DashboardSemanticMember;
  grain?: DashboardTimeGrain;
  `alias`?: DashboardResultField;
}

/** A semantic metric reference. Metric expressions are never part of this type. */
model DashboardMetricReference {
  metric: DashboardSemanticMember;
  `alias`?: DashboardResultField;
}

/** A physical field reference is only valid for a records query root dataset. */
model DashboardRecordFieldReference {
  field: DashboardRecordField;
  `alias`?: DashboardResultField;
}

/** Unaliased selections can use the compact string form; aliased selections use the closed object form. */
union DashboardDimensionSelection {
  name: DashboardSemanticMember,
  reference: DashboardDimensionReference,
}

union DashboardMetricSelection {
  name: DashboardSemanticMember,
  reference: DashboardMetricReference,
}

union DashboardRecordFieldSelection {
  name: DashboardRecordField,
  reference: DashboardRecordFieldReference,
}

enum DashboardSortDirection {
  asc: "asc",
  desc: "desc",
}

model DashboardSort {
  /** A compiled result-frame field name, never a source member or expression. */
  field: DashboardResultField;
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
  dataset: DashboardObjectIdentifier;
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
  group?: DashboardDimensionSelection;
  quantiles: float64[];
  whiskers?: DashboardDistributionWhiskers;
  outliers: DashboardDistributionOutlierPolicy;
  approximation: DashboardHistogramApproximation;
  limit?: int32;
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
  dataset: DashboardObjectIdentifier;
  dependsOn?: DashboardObjectIdentifier[];
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
  id: DashboardObjectIdentifier;
  label: string;
  description?: string;
  dimension: DashboardSemanticMember;
  control: DashboardFilterControl;
  operators?: DashboardFilterOperator[];
  default?: DashboardFilterExpression;
  required?: boolean;
  readerEditable?: boolean;
  targets?: DashboardFilterTarget[];
  urlParameter?: DashboardURLParameter;
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

/** Marks supported by a governed multi-measure combo series. */
enum DashboardComboSeriesMark {
  line: "line",
  area: "area",
  bar: "bar",
  column: "column",
}

/** Axis assignment for one governed combo measure. */
enum DashboardComboSeriesAxis {
  primary: "primary",
  secondary: "secondary",
}

/** One result-frame measure and its governed combo rendering policy. */
model DashboardComboSeries {
  field: DashboardResultField;
  mark: DashboardComboSeriesMark;
  axis: DashboardComboSeriesAxis;
}

/** A renderer-neutral color or icon intent applied by a conditional rule. */
model DashboardConditionalStyle {
  color?: LeapViewVisualization.VisualizationColorIntent;
  icon?: LeapViewVisualization.VisualizationIconIntent;
}

/** One ordered numeric comparison in a rules-based conditional format. */
model DashboardConditionalThreshold {
  operator: LeapViewVisualization.VisualizationComparisonOperator;
  value: float64;
  style: DashboardConditionalStyle;
}

@discriminator("kind")
model DashboardConditionalRule {
  kind: string;
}

model DashboardGradientConditionalRule extends DashboardConditionalRule {
  kind: "gradient";
  minimum: float64;
  maximum: float64;
  low: DashboardConditionalStyle;
  high: DashboardConditionalStyle;
  nullStyle: DashboardConditionalStyle;
}

model DashboardRulesConditionalRule extends DashboardConditionalRule {
  kind: "rules";
  rules: DashboardConditionalThreshold[];
  nullStyle: DashboardConditionalStyle;
  defaultStyle: DashboardConditionalStyle;
}

model DashboardFieldConditionalRule extends DashboardConditionalRule {
  kind: "field";
  source: DashboardResultField;
  values: Record<DashboardConditionalStyle>;
  nullStyle: DashboardConditionalStyle;
  defaultStyle: DashboardConditionalStyle;
}

/** A closed, governed conditional-format binding over compiled result names. */
model DashboardConditionalFormat {
  id: DashboardObjectIdentifier;
  target: LeapViewVisualization.VisualizationConditionalTarget;
  field: DashboardResultField;
  rule: DashboardConditionalRule;
}

/** Renderer-neutral presentation policies; renderer options are intentionally absent. */
@discriminator("type")
model DashboardPresentation {
  type: string;
  conditionalFormatting?: DashboardConditionalFormat[];
  /** Whether cartesian/value axes are rendered for this visual. */
  axisVisible?: boolean;
}

model CartesianDashboardPresentation extends DashboardPresentation {
  type: "cartesian";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  stacking?: DashboardStackingMode;
  orientation?: DashboardOrientation;
  showSymbols?: boolean;
  smooth?: boolean;
  /** Render line segments as discrete steps between categories. */
  step?: boolean;
  dataZoom?: boolean;
  symbolSize?: float64;
  labelPosition?: DashboardLabelPosition;
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
  /** Explicit per-measure marks and axis assignments for combo visuals. */
  series?: DashboardComboSeries[];
}

/** A governed result-frame binding used by a point/scatter visual. */
model PointDashboardColorScale {
  kind: LeapViewVisualization.VisualizationPointColorScaleKind;
  minimum?: float64;
  maximum?: float64;
  scheme?: string;
}

/** Pixel and optional domain bounds for a governed point size binding. */
model PointDashboardSizeScale {
  minimum?: float64;
  maximum?: float64;
  minimumPixels: float64;
  maximumPixels: float64;
}

/** Overplotting policy for a point/scatter visual. */
model PointDashboardOverplot {
  strategy: LeapViewVisualization.VisualizationPointOverplotStrategy;
  opacity?: float64;
  largeMode?: LeapViewVisualization.VisualizationPointLargeMode;
  largeThreshold?: int64;
}

/** Strict point/scatter presentation and its governed result-frame channels. */
model PointDashboardPresentation extends DashboardPresentation {
  type: "point";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  identity: DashboardResultField[];
  x: DashboardResultField;
  y: DashboardResultField;
  size?: DashboardResultField;
  color?: DashboardResultField;
  series?: DashboardResultField;
  label?: DashboardResultField;
  tooltip?: DashboardResultField[];
  colorScale?: PointDashboardColorScale;
  sizeScale?: PointDashboardSizeScale;
  overplot?: PointDashboardOverplot;
  brush?: LeapViewVisualization.VisualizationPointBrushGesture[];
}

model ProportionalDashboardPresentation extends DashboardPresentation {
  type: "proportional";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
  orientation?: DashboardOrientation;
  rose?: boolean;
  centerLabel?: string;
  labelPosition?: DashboardLabelPosition;
  innerRadius?: float64;
  outerRadius?: float64;
  align?: DashboardProportionalAlignment;
  sort?: LeapViewVisualization.VisualizationSortDirection;
}

enum DashboardProportionalAlignment {
  left: "left",
  center: "center",
  right: "right",
}

model HierarchyDashboardPresentation extends DashboardPresentation {
  type: "hierarchy";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  orientation?: DashboardOrientation;
  initialDepth?: int32;
  roam?: boolean;
  layout?: LeapViewVisualization.VisualizationHierarchyLayout;
  breadcrumb?: boolean;
  nodeGap?: float64;
  curveness?: float64;
  focus?: LeapViewVisualization.VisualizationGraphFocus;
}

model PolarDashboardPresentation extends DashboardPresentation {
  type: "polar";
  legend?: DashboardLegendPosition;
  labels?: DashboardLabelPolicy;
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
  minimum?: float64;
  maximum?: float64;
  target?: float64;
  showPointer?: boolean;
  area?: boolean;
  progressWidth?: float64;
  thresholds?: LeapViewVisualization.VisualizationThreshold[];
}

model GeographicDashboardPresentation extends DashboardPresentation {
  type: "geographic";
  labels?: DashboardLabelPolicy;
  theme?: LeapViewVisualization.VisualizationMapTheme;
  basemap?: string;
  labelDensity?: LeapViewVisualization.VisualizationMapLabelDensity;
  camera?: DashboardMapCamera;
  controls?: DashboardMapControls;
  roam?: boolean;
  layers?: DashboardGeographicLayer[];
}

model DashboardMapCamera {
  mode?: LeapViewVisualization.VisualizationMapCameraMode;
  center?: float64[];
  zoom?: float64;
  padding?: int32;
  minimumZoom?: float64;
  maximumZoom?: float64;
}

model DashboardMapControls {
  zoom?: boolean;
  reset?: boolean;
  compass?: boolean;
}

enum DashboardGeographicLayerKind {
  point: "point",
  choropleth: "choropleth",
  heat: "heat",
  density: "density",
  reference: "reference",
  path: "path",
}

model DashboardMapColorScale {
  kind?: LeapViewVisualization.VisualizationMapColorScaleKind;
  palette?: string;
  reverse?: boolean;
  domainMinimum?: float64;
  domainMidpoint?: float64;
  domainMaximum?: float64;
  nullColor?: string;
}

model DashboardMapSizeScale {
  minimumRadius?: float64;
  maximumRadius?: float64;
  domainMinimum?: float64;
  domainMaximum?: float64;
}

model DashboardMapStroke {
  color?: string;
  width?: float64;
  opacity?: float64;
}

model DashboardMapHeatStyle {
  radius?: float64;
  intensity?: float64;
}

model DashboardMapLineStyle {
  width?: float64;
  curvature?: float64;
}

model DashboardMapCluster {
  enabled?: boolean;
  radius?: int32;
  maximumZoom?: int32;
  minimumPoints?: int32;
  showCount?: boolean;
}

model DashboardGeographicLayerOptions {
  id: DashboardObjectIdentifier;
  label?: DashboardResultField;
  tooltip?: DashboardResultField[];
  position?: LeapViewVisualization.VisualizationMapLayerPosition;
  minimumZoom?: float64;
  maximumZoom?: float64;
}

@discriminator("kind")
model DashboardGeographicLayer extends DashboardGeographicLayerOptions {
  kind: string;
}

model DashboardPointGeographicLayer extends DashboardGeographicLayer {
  kind: "point";
  latitude: DashboardResultField;
  longitude: DashboardResultField;
  value?: DashboardResultField;
  category?: DashboardResultField;
  size?: DashboardMapSizeScale;
  color?: DashboardMapColorScale;
  stroke?: DashboardMapStroke;
  cluster?: DashboardMapCluster;
  opacity?: float64;
}

model DashboardChoroplethGeographicLayer extends DashboardGeographicLayer {
  kind: "choropleth";
  geometryAsset: string;
  join: DashboardResultField;
  value?: DashboardResultField;
  category?: DashboardResultField;
  color?: DashboardMapColorScale;
  stroke?: DashboardMapStroke;
  opacity?: float64;
}

model DashboardReferenceGeographicLayer extends DashboardGeographicLayer {
  kind: "reference";
  geometryAsset: string;
  color?: DashboardMapColorScale;
  stroke?: DashboardMapStroke;
  opacity?: float64;
}

model DashboardHeatGeographicLayer extends DashboardGeographicLayer {
  kind: "heat";
  latitude: DashboardResultField;
  longitude: DashboardResultField;
  value?: DashboardResultField;
  color?: DashboardMapColorScale;
  heat?: DashboardMapHeatStyle;
  opacity?: float64;
}

model DashboardDensityGeographicLayer extends DashboardGeographicLayer {
  kind: "density";
  latitude: DashboardResultField;
  longitude: DashboardResultField;
  value?: DashboardResultField;
  color?: DashboardMapColorScale;
  heat?: DashboardMapHeatStyle;
  opacity?: float64;
}

model DashboardPathGeographicLayer extends DashboardGeographicLayer {
  kind: "path";
  latitude: DashboardResultField;
  longitude: DashboardResultField;
  path: DashboardResultField;
  order: DashboardResultField;
  value?: DashboardResultField;
  category?: DashboardResultField;
  color?: DashboardMapColorScale;
  stroke?: DashboardMapStroke;
  line?: DashboardMapLineStyle;
  opacity?: float64;
}

model TableDashboardPresentation extends DashboardPresentation {
  type: "table";
  rowHeight: int32;
  showHeader: boolean;
  striped: boolean;
}

model KPIDashboardPresentation extends DashboardPresentation {
  type: "kpi";
  mode?: LeapViewVisualization.VisualizationKPIMode;
  delta?: LeapViewVisualization.VisualizationKPIDeltaMode;
  favorableDirection?: LeapViewVisualization.VisualizationKPIDirection;
  missingComparison?: LeapViewVisualization.VisualizationKPIMissingComparison;
  ranges?: LeapViewVisualization.VisualizationKPIQualitativeRange[];
  thresholds?: LeapViewVisualization.VisualizationThreshold[];
  comparison?: DashboardKPIValueBinding;
  goal?: DashboardKPIValueBinding;
  trend?: DashboardKPITrendBinding;
  displayUnits?: LeapViewVisualization.VisualizationDisplayUnits;
  note?: string;
  tone?: LeapViewVisualization.VisualizationTone;
}

/** A governed numeric result used for a KPI comparison or goal. */
model DashboardKPIValueBinding {
  dataset: DashboardObjectIdentifier;
  field: DashboardResultField;
  reducer?: LeapViewVisualization.VisualizationReferenceReducer;
  label: string;
}

/** A governed result frame used for a KPI sparkline trend. */
model DashboardKPITrendBinding {
  dataset: DashboardObjectIdentifier;
  category: DashboardResultField;
  value: DashboardResultField;
}

enum DashboardLabelPosition {
  automatic: "automatic",
  inside: "inside",
  outside: "outside",
  top: "top",
}

model DashboardTextBinding {
  dataset: DashboardObjectIdentifier;
  field: DashboardResultField;
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
  id: DashboardObjectIdentifier;
  label?: string;
  template: LeapViewVisualization.VisualizationCalculationTemplate;
  source: DashboardResultField;
  axis?: LeapViewVisualization.VisualizationCalculationAxis;
  orderBy?: DashboardSort[];
  partitionBy?: DashboardResultField[];
  reset?: LeapViewVisualization.VisualizationCalculationReset;
  window?: int32;
  offset?: int32;
  parent?: DashboardObjectIdentifier;
  lookup?: DashboardCalculationLookup;
  hidden?: boolean;
}

model DashboardCalculationLookup {
  field: DashboardResultField;
  value: string;
}

model DashboardVisual {
  type: DashboardVisualType;
  title?: string;
  /** Controls whether the authored title is shown in the visual frame. */
  titleVisible?: boolean;
  subtitle?: string;
  description?: string;
  query: DashboardQuery;
  @apigen.propertyNames(DashboardObjectIdentifier)
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
  targets?: DashboardObjectIdentifier[];
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
  highlightTargets?: DashboardObjectIdentifier[];
  noneTargets?: DashboardObjectIdentifier[];
}

model SpatialSelectionDashboardInteraction extends DashboardInteraction {
  type: "spatialSelection";
  gestures: DashboardSpatialGesture[];
  latitude: DashboardSpatialMapping;
  longitude: DashboardSpatialMapping;
  highlightTargets?: DashboardObjectIdentifier[];
  noneTargets?: DashboardObjectIdentifier[];
}

enum DashboardSpatialGesture {
  box: "box",
  lasso: "lasso",
  radius: "radius",
}

model DashboardInteractionMapping {
  field: DashboardResultField;
  dataset?: DashboardObjectIdentifier;
  grain?: DashboardTimeGrain;
  value: DashboardResultField;
  label?: string;
}

model DashboardSpatialMapping {
  source: DashboardResultField;
  field: DashboardResultField;
  dataset?: DashboardObjectIdentifier;
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
  id: DashboardObjectIdentifier;
  type: string;
  placement: DashboardPlacement;
}

model VisualDashboardPageComponent extends DashboardPageComponent {
  type: "visual";
  visual: DashboardObjectIdentifier;
}

model FilterDashboardPageComponent extends DashboardPageComponent {
  type: "filter";
  filter: DashboardObjectIdentifier;
}

model HeaderDashboardPageComponent extends DashboardPageComponent {
  type: "header";
  title?: string;
  description?: string;
}

/**
 * A page-scoped state binding for one governed dashboard filter definition.
 * The binding identity is local to its page and is independent from any
 * in-canvas filter component that presents the same filter.
 */
model DashboardPageFilterBinding {
  id: DashboardObjectIdentifier;
  filter: DashboardObjectIdentifier;
  default?: DashboardFilterExpression;
  required?: boolean;
  readerEditable?: boolean;
  targets?: DashboardObjectIdentifier[];
  urlParameter?: DashboardURLParameter;
}

model DashboardPage {
  id: DashboardObjectIdentifier;
  title: string;
  description?: string;
  layout?: DashboardLayoutOverride;
  filterBindings?: DashboardPageFilterBinding[];
  components: DashboardPageComponent[];
}
