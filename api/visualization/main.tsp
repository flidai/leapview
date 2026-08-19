import "@yacobolo/apigen";

@apigen.`package`(#{
  title: "LeapView Visualization IR",
  version: "1.0.0",
  description: "Renderer-independent visualization specifications and runtime data contracts.",
})
namespace LeapViewVisualization;

enum VisualizationFieldRole {
  dimension: "dimension",
  metric: "metric",
  metadata: "metadata",
  identity: "identity",
}

enum VisualizationDataType {
  string: "string",
  boolean: "boolean",
  integer: "integer",
  decimal: "decimal",
  float: "float",
  temporal: "temporal",
  date: "date",
  geographic: "geographic",
}

enum VisualizationCompleteness {
  complete: "complete",
  truncated: "truncated",
  partial: "partial",
  empty: "empty",
}

enum VisualizationTemporalMeaning {
  instant: "instant",
  bucket: "bucket",
}

enum VisualizationWeekStart {
  monday: "monday",
  tuesday: "tuesday",
  wednesday: "wednesday",
  thursday: "thursday",
  friday: "friday",
  saturday: "saturday",
  sunday: "sunday",
}

enum VisualizationCartesianMark {
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

enum VisualizationPointColorScaleKind {
  categorical: "categorical",
  quantitative: "quantitative",
}

enum VisualizationPointOverplotStrategy {
  showAll: "show_all",
  opacity: "opacity",
}

enum VisualizationPointLargeMode {
  automatic: "automatic",
  always: "always",
  neverMode: "never",
}

enum VisualizationPointBrushGesture {
  rectangle: "rectangle",
  lasso: "lasso",
}

enum VisualizationProportionalMark {
  pie: "pie",
  donut: "donut",
  funnel: "funnel",
}

enum VisualizationHierarchyMark {
  treemap: "treemap",
  sunburst: "sunburst",
  tree: "tree",
  sankey: "sankey",
  graph: "graph",
}

enum VisualizationPolarMark {
  radar: "radar",
  gauge: "gauge",
}

enum VisualizationGeographicLayerKind {
  choropleth: "choropleth",
  point: "point",
  heat: "heat",
  density: "density",
  reference: "reference",
  path: "path",
}

enum VisualizationMapTheme {
  auto: "auto",
  light: "light",
  dark: "dark",
}

enum VisualizationMapLabelDensity {
  hidden: "hidden",
  normal: "normal",
  dense: "dense",
}

enum VisualizationDisplayUnits {
  auto: "auto",
  none: "none",
  thousands: "thousands",
  millions: "millions",
  billions: "billions",
  trillions: "trillions",
}

enum VisualizationLabelDensity {
  hidden: "hidden",
  automatic: "automatic",
  dense: "dense",
  always: "always",
}

enum VisualizationLabelPriority {
  selected: "selected",
  anomaly: "anomaly",
  threshold: "threshold",
}

enum VisualizationMapCameraMode {
  fitData: "fit_data",
  fixed: "fixed",
  preserve: "preserve",
}

enum VisualizationMapLayerPosition {
  belowLabels: "below_labels",
  aboveLabels: "above_labels",
}

enum VisualizationMapColorScaleKind {
  sequential: "sequential",
  diverging: "diverging",
  categorical: "categorical",
}

enum VisualizationInteractionKind {
  select: "select",
  filter: "filter",
  drill: "drill",
  navigate: "navigate",
}

enum VisualizationSelectionMode {
  single: "single",
  multiple: "multiple",
}

enum VisualizationInteractionEffect {
  none: "none",
  filter: "filter",
  highlight: "highlight",
}

enum VisualizationKPIMode {
  compact: "compact",
  bullet: "bullet",
  progress: "progress",
}

enum VisualizationKPIDeltaMode {
  absolute: "absolute",
  relative: "relative",
}

enum VisualizationKPIDirection {
  increase: "increase",
  decrease: "decrease",
  neutral: "neutral",
}

enum VisualizationKPIMissingComparison {
  showUnavailable: "show_unavailable",
  hide: "hide",
}

enum VisualizationTone {
  neutral: "neutral",
  ink: "ink",
  success: "success",
  warning: "warning",
  danger: "danger",
}

enum VisualizationLabelPosition {
  automatic: "automatic",
  inside: "inside",
  outside: "outside",
  top: "top",
}

enum VisualizationHierarchyLayout {
  standard: "standard",
  circular: "circular",
}

enum VisualizationGraphFocus {
  none: "none",
  adjacency: "adjacency",
}

enum VisualizationAxis {
  primary: "primary",
  secondary: "secondary",
}

enum VisualizationCartesianAxis {
  x: "x",
  primaryY: "primary_y",
  secondaryY: "secondary_y",
}

enum VisualizationAxisScale {
  automatic: "automatic",
  linear: "linear",
  log: "log",
}

enum VisualizationAxisZeroPolicy {
  automatic: "automatic",
  include: "include",
  exclude: "exclude",
}

enum VisualizationAxisTickDensity {
  automatic: "automatic",
  sparse: "sparse",
  normal: "normal",
  dense: "dense",
}

enum VisualizationReferenceReducer {
  first: "first",
  last: "last",
  minimum: "minimum",
  maximum: "maximum",
  mean: "mean",
  median: "median",
}

enum VisualizationStackingMode {
  none: "none",
  normal: "normal",
  percent: "percent",
}

enum VisualizationColorIntent {
  accent: "accent",
  neutral: "neutral",
  ink: "ink",
  success: "success",
  warning: "warning",
  danger: "danger",
  data1: "data_1",
  data2: "data_2",
  data3: "data_3",
  data4: "data_4",
  data5: "data_5",
  data6: "data_6",
  data7: "data_7",
  data8: "data_8",
}

enum VisualizationConditionalTarget {
  markFill: "mark_fill",
  markStroke: "mark_stroke",
  seriesColor: "series_color",
  labelForeground: "label_foreground",
  visualBackground: "visual_background",
  cellForeground: "cell_foreground",
  cellBackground: "cell_background",
  kpiValue: "kpi_value",
  icon: "icon",
}

enum VisualizationIconIntent {
  circle: "circle",
  square: "square",
  diamond: "diamond",
  triangleUp: "triangle_up",
  triangleDown: "triangle_down",
  arrowUp: "arrow_up",
  arrowDown: "arrow_down",
  warning: "warning",
}

enum VisualizationComparisonOperator {
  lessThan: "less_than",
  lessOrEqual: "less_or_equal",
  greaterThan: "greater_than",
  greaterOrEqual: "greater_or_equal",
  equal: "equal",
  notEqual: "not_equal",
}

enum VisualizationOrientation {
  horizontal: "horizontal",
  vertical: "vertical",
}

enum VisualizationLegendPosition {
  hidden: "hidden",
  top: "top",
  right: "right",
  bottom: "bottom",
  left: "left",
}

enum VisualizationSortDirection {
  ascending: "ascending",
  descending: "descending",
}

enum VisualizationCalculationTemplate {
  runningTotal: "running_total",
  movingAverage: "moving_average",
  difference: "difference",
  percentageDifference: "percentage_difference",
  percentOfParent: "percent_of_parent",
  percentOfGrandTotal: "percent_of_grand_total",
  rank: "rank",
  cumulativeContribution: "cumulative_contribution",
  lookup: "lookup",
}

enum VisualizationCalculationAxis {
  rows: "rows",
  columns: "columns",
  hierarchy: "hierarchy",
  facets: "facets",
}

enum VisualizationCalculationReset {
  none: "none",
  highestParent: "highest_parent",
  lowestParent: "lowest_parent",
}

enum VisualizationFieldProvenanceKind {
  modeled: "modeled",
  aggregated: "aggregated",
  visualCalculation: "visual_calculation",
}

enum VisualizationCardinalityKind {
  unknownValue: "unknown",
  lowerBound: "lower_bound",
  estimated: "estimated",
  exact: "exact",
}

enum VisualizationSpatialPrecision {
  raw: "raw",
  aggregated: "aggregated",
}

enum VisualizationSpatialSelectionGesture {
  box: "box",
  lasso: "lasso",
  radius: "radius",
}

enum VisualizationDataStateKind {
  inline: "inline",
  windowed: "windowed",
  spatialTiled: "spatial_tiled",
}

enum VisualizationDataStateTransportEncoding {
  json: "json",
}

enum VisualizationStatusKind {
  idle: "idle",
  loading: "loading",
  ready: "ready",
  noData: "no_data",
  partial: "partial",
  error: "error",
}

enum VisualizationDiagnosticSeverity {
  info: "info",
  warning: "warning",
  error: "error",
}

model VisualizationTemporalMetadata {
  `grain`: string;
  `timezone`: string;
  `calendar`: string;
  `weekStart`: VisualizationWeekStart;
  `meaning`: VisualizationTemporalMeaning;
}

model VisualizationGeographicMetadata {
  `identifierSystem`: string;
  `geometryAsset`: string;
  `joinField`: string;
  `unmatchedPolicy`: string;
}

@discriminator("kind")
model VisualizationFormat {
  `kind`: string;
}

model NumberVisualizationFormat extends VisualizationFormat {
  `kind`: "number";
  `minimumFractionDigits`?: int32;
  `maximumFractionDigits`?: int32;
}

model CurrencyVisualizationFormat extends VisualizationFormat {
  `kind`: "currency";
  `currency`: string;
  `minimumFractionDigits`?: int32;
  `maximumFractionDigits`?: int32;
}

model PercentVisualizationFormat extends VisualizationFormat {
  `kind`: "percent";
  `minimumFractionDigits`?: int32;
  `maximumFractionDigits`?: int32;
}

model CompactVisualizationFormat extends VisualizationFormat {
  `kind`: "compact";
  `maximumFractionDigits`?: int32;
}

model DurationVisualizationFormat extends VisualizationFormat {
  `kind`: "duration";
  `unit`: string;
}

model TemporalVisualizationFormat extends VisualizationFormat {
  `kind`: "temporal";
  `dateStyle`?: string;
  `timeStyle`?: string;
}

model VisualizationGridFieldMetadata {
  `group`?: string;
  `metric`?: string;
  `columnValue`?: string;
  `formatting`: TableVisualizationFormattingRule[];
}

model VisualizationFieldProvenance {
  `kind`: VisualizationFieldProvenanceKind;
  `sourceRefs`: string[];
  `calculationID`?: string;
}

model VisualizationField {
  `id`: string;
  `sourceRef`?: string;
  `role`: VisualizationFieldRole;
  `dataType`: VisualizationDataType;
  `nullable`: boolean;
  `label`: string;
  `format`?: VisualizationFormat;
  `time`?: VisualizationTemporalMetadata;
  `geographic`?: VisualizationGeographicMetadata;
  `grid`?: VisualizationGridFieldMetadata;
  `provenance`?: VisualizationFieldProvenance;
}

model VisualizationDatasetSchema {
  `id`: string;
  `fields`: VisualizationField[];
}

model VisualizationFieldRef {
  `dataset`: string;
  `field`: string;
}

model VisualizationCalculationOrder {
  `field`: VisualizationFieldRef;
  `direction`: VisualizationSortDirection;
}

model VisualizationCalculationLookup {
  `field`: VisualizationFieldRef;
  `value`: string;
}

model VisualizationCalculation {
  `id`: string;
  `label`: string;
  `dataset`: string;
  `template`: VisualizationCalculationTemplate;
  `source`: VisualizationFieldRef;
  `axis`: VisualizationCalculationAxis;
  `orderBy`: VisualizationCalculationOrder[];
  `partitionBy`: VisualizationFieldRef[];
  `reset`: VisualizationCalculationReset;
  `window`?: int64;
  `offset`?: int64;
  `parent`?: VisualizationFieldRef;
  `lookup`?: VisualizationCalculationLookup;
  `hidden`: boolean;
  `format`?: VisualizationFormat;
}

@discriminator("kind")
model VisualizationReferenceValue {
  `kind`: string;
}

model NumberVisualizationReferenceValue extends VisualizationReferenceValue {
  `kind`: "number";
  `value`: float64;
}

model TextVisualizationReferenceValue extends VisualizationReferenceValue {
  `kind`: "text";
  `value`: string;
}

model FieldVisualizationReferenceValue extends VisualizationReferenceValue {
  `kind`: "field";
  `field`: VisualizationFieldRef;
  `reducer`: VisualizationReferenceReducer;
}

model VisualizationAxisConfiguration {
  `id`: VisualizationCartesianAxis;
  `title`?: string;
  `scale`: VisualizationAxisScale;
  `zero`: VisualizationAxisZeroPolicy;
  `minimum`?: float64;
  `maximum`?: float64;
  `unit`?: string;
  `displayUnits`?: VisualizationDisplayUnits;
  `tickDensity`: VisualizationAxisTickDensity;
}

model VisualizationReferenceLine {
  `id`: string;
  `axis`: VisualizationCartesianAxis;
  `value`: VisualizationReferenceValue;
  `label`?: string;
  `tone`: VisualizationTone;
}

model VisualizationReferenceBand {
  `id`: string;
  `axis`: VisualizationCartesianAxis;
  `from`: VisualizationReferenceValue;
  `to`: VisualizationReferenceValue;
  `label`?: string;
  `tone`: VisualizationTone;
}

model VisualizationEventAnnotation {
  `id`: string;
  `axis`: VisualizationCartesianAxis;
  `value`: VisualizationReferenceValue;
  `label`: string;
  `description`?: string;
  `tone`: VisualizationTone;
}

model VisualizationConditionalStyle {
  `color`?: VisualizationColorIntent;
  `icon`?: VisualizationIconIntent;
}

model VisualizationConditionalThreshold {
  `operator`: VisualizationComparisonOperator;
  `value`: float64;
  `style`: VisualizationConditionalStyle;
}

@discriminator("kind")
model VisualizationConditionalRule {
  `kind`: string;
}

model GradientVisualizationConditionalRule extends VisualizationConditionalRule {
  `kind`: "gradient";
  `minimum`: float64;
  `maximum`: float64;
  `low`: VisualizationConditionalStyle;
  `high`: VisualizationConditionalStyle;
  `nullStyle`: VisualizationConditionalStyle;
}

model RulesVisualizationConditionalRule extends VisualizationConditionalRule {
  `kind`: "rules";
  `rules`: VisualizationConditionalThreshold[];
  `nullStyle`: VisualizationConditionalStyle;
  `defaultStyle`: VisualizationConditionalStyle;
}

model FieldVisualizationConditionalRule extends VisualizationConditionalRule {
  `kind`: "field";
  `source`: VisualizationFieldRef;
  `values`: Record<VisualizationConditionalStyle>;
  `nullStyle`: VisualizationConditionalStyle;
  `defaultStyle`: VisualizationConditionalStyle;
}

model VisualizationConditionalFormat {
  `id`: string;
  `target`: VisualizationConditionalTarget;
  `field`: VisualizationFieldRef;
  `rule`: VisualizationConditionalRule;
}

model VisualizationTextBinding {
  `field`: VisualizationFieldRef;
  `reducer`: VisualizationReferenceReducer;
  `prefix`: string;
  `suffix`: string;
  `fallback`: string;
}

model VisualizationMetadataBindings {
  `title`?: VisualizationTextBinding;
  `subtitle`?: VisualizationTextBinding;
  `description`?: VisualizationTextBinding;
  `summary`?: VisualizationTextBinding;
}

model VisualizationDataBudget {
  `maxRows`: int64;
  `requiredCompleteness`: VisualizationCompleteness;
}

model VisualizationAccessibility {
  `title`: string;
  `description`: string;
  `summary`?: string;
  `announceChanges`?: boolean;
}

model VisualizationInteractionMapping {
  `source`: VisualizationFieldRef;
  `targetFieldID`: string;
  `targetDatasetID`?: string;
  `grain`?: string;
  `label`?: VisualizationFieldRef;
}

model VisualizationInteractionTarget {
  `visualID`: string;
  `effect`: VisualizationInteractionEffect;
}

model VisualizationInteraction {
  @doc("Stable compiler-owned interaction identity. Commands and persisted selections must reference this exact value.")
  `id`: string;
  `kind`: VisualizationInteractionKind;
  `mappings`: VisualizationInteractionMapping[];
  `targets`: VisualizationInteractionTarget[];
  `mode`: VisualizationSelectionMode;
  `requiresStableIdentity`: boolean;
}

model VisualizationLabelPolicy {
  `density`: VisualizationLabelDensity;
  `priority`: VisualizationLabelPriority[];
  `maxCharacters`: int32;
  `minimumSpacing`: int32;
  `tooltipFallback`: boolean;
}

model VisualizationPresentation {
  `legend`: VisualizationLegendPosition;
  `labelPolicy`: VisualizationLabelPolicy;
  `displayUnits`?: VisualizationDisplayUnits;
}

model VisualizationThreshold {
  `value`: float64;
  `tone`: VisualizationTone;
}

model VisualizationComboSeries {
  `seriesValue`: string;
  `mark`: VisualizationCartesianMark;
  `axis`: VisualizationAxis;
}

model VisualizationSeriesIntent {
  `value`: string;
  `order`?: int32;
  `color`?: VisualizationColorIntent;
}

model CartesianVisualizationPresentation extends VisualizationPresentation {
  `smooth`: boolean;
  `stacked`: boolean;
  `showSymbols`: boolean;
  `dataZoom`: boolean;
  `area`: boolean;
  `step`: boolean;
  `orientation`?: VisualizationOrientation;
  `labelPosition`?: VisualizationLabelPosition;
  `symbolSize`?: float64;
  `histogramBins`?: int32;
  `comboSeries`?: VisualizationComboSeries[];
  `stacking`?: VisualizationStackingMode;
  `seriesIntent`?: VisualizationSeriesIntent[];
}

model PointVisualizationColorScale {
  `kind`: VisualizationPointColorScaleKind;
  `minimum`?: float64;
  `maximum`?: float64;
  `scheme`?: string;
}

model PointVisualizationSizeScale {
  `minimum`?: float64;
  `maximum`?: float64;
  `minimumPixels`: float64;
  `maximumPixels`: float64;
}

model PointVisualizationPresentation extends VisualizationPresentation {
  `overplot`: VisualizationPointOverplotStrategy;
  `opacity`: float64;
  `largeMode`: VisualizationPointLargeMode;
  `largeThreshold`: int64;
  `brush`: VisualizationPointBrushGesture[];
}

model ProportionalVisualizationPresentation extends VisualizationPresentation {
  `orientation`: VisualizationOrientation;
  `rose`: boolean;
  `centerLabel`?: string;
  `labelPosition`?: VisualizationLabelPosition;
  `innerRadius`?: float64;
  `outerRadius`?: float64;
  `align`?: string;
  `sort`?: VisualizationSortDirection;
}

model HierarchyVisualizationPresentation extends VisualizationPresentation {
  `orientation`: VisualizationOrientation;
  `initialDepth`?: int32;
  `roam`: boolean;
  `layout`?: VisualizationHierarchyLayout;
  `breadcrumb`?: boolean;
  `nodeGap`?: float64;
  `curveness`?: float64;
  `focus`?: VisualizationGraphFocus;
}

model PolarVisualizationPresentation extends VisualizationPresentation {
  `minimum`?: float64;
  `maximum`?: float64;
  `target`?: float64;
  `showPointer`: boolean;
  `area`?: boolean;
  `progressWidth`?: float64;
  `thresholds`?: VisualizationThreshold[];
}

model GridVisualizationPresentation {
  `rowHeight`: int64;
  `striped`: boolean;
  `showHeader`: boolean;
}

model KPIVisualizationPresentation {
  `mode`: VisualizationKPIMode;
  `delta`: VisualizationKPIDeltaMode;
  `favorableDirection`: VisualizationKPIDirection;
  `missingComparison`: VisualizationKPIMissingComparison;
  `ranges`: VisualizationKPIQualitativeRange[];
  `displayUnits`?: VisualizationDisplayUnits;
  `note`?: string;
  `tone`?: VisualizationTone;
  `thresholds`?: VisualizationThreshold[];
}

model VisualizationKPIValueBinding {
  `field`: VisualizationFieldRef;
  `reducer`: VisualizationReferenceReducer;
  `label`: string;
}

model VisualizationKPITrendBinding {
  `category`: VisualizationFieldRef;
  `value`: VisualizationFieldRef;
}

model VisualizationKPIQualitativeRange {
  `minimum`?: float64;
  `maximum`?: float64;
  `label`: string;
  `tone`: VisualizationTone;
}

model GeographicVisualizationPresentation extends VisualizationPresentation {
  `roam`: boolean;
  `basemap`?: VisualizationMapStyleAsset;
  `theme`: VisualizationMapTheme;
  `labelDensity`: VisualizationMapLabelDensity;
  `camera`: VisualizationMapCamera;
  `controls`: VisualizationMapControls;
}

@apigen.contract(#{ kind: "visualization-spec", tags: #["visualization", "ir"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "visualization-spec" })
@discriminator("kind")
model VisualizationSpec {
  `kind`: string;
  `title`: string;
  `subtitle`?: string;
  `datasets`: VisualizationDatasetSchema[];
  `dataBudget`: VisualizationDataBudget;
  `accessibility`: VisualizationAccessibility;
  `interactions`: VisualizationInteraction[];
  `conditionalFormatting`?: VisualizationConditionalFormat[];
  `metadataBindings`?: VisualizationMetadataBindings;
  `calculations`?: VisualizationCalculation[];
}

model CartesianVisualizationSpec extends VisualizationSpec {
  `kind`: "cartesian";
  `mark`: VisualizationCartesianMark;
  `x`: VisualizationFieldRef;
  `y`: VisualizationFieldRef[];
  `series`?: VisualizationFieldRef;
  `axes`?: VisualizationAxisConfiguration[];
  `referenceLines`?: VisualizationReferenceLine[];
  `referenceBands`?: VisualizationReferenceBand[];
  `eventAnnotations`?: VisualizationEventAnnotation[];
  `tooltip`?: VisualizationFieldRef[];
  `presentation`: CartesianVisualizationPresentation;
}

model PointVisualizationSpec extends VisualizationSpec {
  `kind`: "point";
  `identity`: VisualizationFieldRef[];
  `x`: VisualizationFieldRef;
  `y`: VisualizationFieldRef;
  `size`?: VisualizationFieldRef;
  `color`?: VisualizationFieldRef;
  `series`?: VisualizationFieldRef;
  `label`?: VisualizationFieldRef;
  `tooltip`?: VisualizationFieldRef[];
  `colorScale`?: PointVisualizationColorScale;
  `sizeScale`?: PointVisualizationSizeScale;
  `axes`?: VisualizationAxisConfiguration[];
  `referenceLines`?: VisualizationReferenceLine[];
  `referenceBands`?: VisualizationReferenceBand[];
  `eventAnnotations`?: VisualizationEventAnnotation[];
  `presentation`: PointVisualizationPresentation;
}

model ProportionalVisualizationSpec extends VisualizationSpec {
  `kind`: "proportional";
  `mark`: VisualizationProportionalMark;
  `category`: VisualizationFieldRef;
  `value`: VisualizationFieldRef;
  `series`?: VisualizationFieldRef;
  `presentation`: ProportionalVisualizationPresentation;
}

model HierarchyVisualizationSpec extends VisualizationSpec {
  `kind`: "hierarchy";
  `mark`: VisualizationHierarchyMark;
  `node`: VisualizationFieldRef;
  `parent`?: VisualizationFieldRef;
  `source`?: VisualizationFieldRef;
  `target`?: VisualizationFieldRef;
  `value`?: VisualizationFieldRef;
  `presentation`: HierarchyVisualizationPresentation;
}

model PolarVisualizationSpec extends VisualizationSpec {
  `kind`: "polar";
  `mark`: VisualizationPolarMark;
  `category`?: VisualizationFieldRef;
  `value`: VisualizationFieldRef;
  `series`?: VisualizationFieldRef;
  `presentation`: PolarVisualizationPresentation;
}

@discriminator("kind")
model TableVisualizationFormattingRule {
  `kind`: string;
}

model TableBadgeFormattingRule extends TableVisualizationFormattingRule {
  `kind`: "badge";
  `values`: Record<string>;
}

model TableTextColorFormattingRule extends TableVisualizationFormattingRule {
  `kind`: "text_color";
  `color`: string;
  `values`?: Record<string>;
  `minimum`?: float64;
  `maximum`?: float64;
}

model TableBackgroundScaleFormattingRule extends TableVisualizationFormattingRule {
  `kind`: "background_scale";
  `minimum`?: float64;
  `maximum`?: float64;
  `lowColor`?: string;
  `highColor`?: string;
}

model TableDataBarFormattingRule extends TableVisualizationFormattingRule {
  `kind`: "data_bar";
  `minimum`?: float64;
  `maximum`?: float64;
  `color`: string;
  `background`?: string;
}

model TableVisualizationColumn {
  `field`: VisualizationFieldRef;
  `label`: string;
  `width`?: int64;
  `group`?: string;
  `metric`?: string;
  `columnValue`?: string;
  `formatting`: TableVisualizationFormattingRule[];
}

model VisualizationSort {
  `field`: VisualizationFieldRef;
  `direction`: VisualizationSortDirection;
}

model TableVisualizationSpec extends VisualizationSpec {
  `kind`: "table";
  `columns`: TableVisualizationColumn[];
  `defaultSort`?: VisualizationSort[];
  `presentation`: GridVisualizationPresentation;
}

model MatrixVisualizationSpec extends VisualizationSpec {
  `kind`: "matrix";
  `rows`: VisualizationFieldRef[];
  `columns`: VisualizationFieldRef[];
  `metrics`: VisualizationFieldRef[];
  `metricFormatting`: Record<TableVisualizationFormattingRule[]>;
  `presentation`: GridVisualizationPresentation;
}

model PivotVisualizationSpec extends VisualizationSpec {
  `kind`: "pivot";
  `rows`: VisualizationFieldRef[];
  `columns`: VisualizationFieldRef[];
  `metrics`: VisualizationFieldRef[];
  `metricFormatting`: Record<TableVisualizationFormattingRule[]>;
  `presentation`: GridVisualizationPresentation;
}

model KPIVisualizationSpec extends VisualizationSpec {
  `kind`: "kpi";
  `value`: VisualizationFieldRef;
  `comparison`?: VisualizationKPIValueBinding;
  `goal`?: VisualizationKPIValueBinding;
  `trend`?: VisualizationKPITrendBinding;
  `presentation`: KPIVisualizationPresentation;
}

model VisualizationGeometryAsset {
  `id`: string;
  `digest`: string;
  `source`: string;
  `license`: string;
  `attribution`: string;
  `identifierSystem`: string;
  `url`: string;
}

model VisualizationMapStyleAsset {
  `id`: string;
  `styleUrl`: string;
  `styleDigest`: string;
  `archiveUrl`: string;
  `archiveDigest`: string;
  `glyphsUrl`: string;
  `spriteUrl`: string;
  `source`: string;
  `license`: string;
  `attribution`: string;
  `minimumZoom`: float64;
  `maximumZoom`: float64;
  `bounds`: float64[];
  `labelAnchor`: string;
}

model VisualizationMapCamera {
  `mode`: VisualizationMapCameraMode;
  `center`?: float64[];
  `zoom`?: float64;
  `padding`: int32;
  `minimumZoom`: float64;
  `maximumZoom`: float64;
}

model VisualizationMapControls {
  `zoom`: boolean;
  `reset`: boolean;
  `compass`: boolean;
}

model VisualizationMapVisibility {
  `minimumZoom`: float64;
  `maximumZoom`: float64;
}

model VisualizationMapSizeScale {
  `minimumRadius`: float64;
  `maximumRadius`: float64;
  `domainMinimum`?: float64;
  `domainMaximum`?: float64;
}

model VisualizationMapColorScale {
  `kind`: VisualizationMapColorScaleKind;
  `palette`: string;
  `reverse`: boolean;
  `domainMinimum`?: float64;
  `domainMidpoint`?: float64;
  `domainMaximum`?: float64;
  `nullColor`: string;
}

model VisualizationMapStroke {
  `color`: string;
  `width`: float64;
  `opacity`: float64;
}

model VisualizationMapCluster {
  `enabled`: boolean;
  `radius`: int32;
  `maximumZoom`: int32;
  `minimumPoints`: int32;
  `showCount`: boolean;
}

model VisualizationMapHeatStyle {
  `radius`: float64;
  `intensity`: float64;
}

model VisualizationMapLineStyle {
  `width`: float64;
  `curvature`: float64;
}

model VisualizationSpatialFieldMapping {
  `source`: VisualizationFieldRef;
  `targetFieldID`: string;
  `targetDatasetID`?: string;
}

model VisualizationSpatialSelectionInteraction {
  `id`: string;
  `gestures`: VisualizationSpatialSelectionGesture[];
  `latitude`: VisualizationSpatialFieldMapping;
  `longitude`: VisualizationSpatialFieldMapping;
  `targets`: VisualizationInteractionTarget[];
}

model VisualizationSpatialCoordinate {
  `longitude`: float64;
  `latitude`: float64;
}

@discriminator("kind")
model VisualizationSpatialSelectionGeometry {
  `kind`: string;
}

model VisualizationSpatialBoxSelection extends VisualizationSpatialSelectionGeometry {
  `kind`: "box";
  `bounds`: VisualizationSpatialBounds;
}

model VisualizationSpatialLassoSelection extends VisualizationSpatialSelectionGeometry {
  `kind`: "lasso";
  `points`: VisualizationSpatialCoordinate[];
}

model VisualizationSpatialRadiusSelection extends VisualizationSpatialSelectionGeometry {
  `kind`: "radius";
  `center`: VisualizationSpatialCoordinate;
  `radiusMeters`: float64;
}

model VisualizationSpatialSelectionState {
  `visualID`: string;
  `interactionID`: string;
  `geometry`: VisualizationSpatialSelectionGeometry;
}

@apigen.contract(#{ kind: "visualization-command", tags: #["visualization", "interaction"] })
model VisualizationSpatialSelectionCommand {
  `visualID`: string;
  `specRevision`: string;
  `dataRevision`: int64;
  `servingStateID`: string;
  `filterRevision`: int64;
  `interactionRevision`: int64;
  `interactionID`: string;
  `action`: "set" | "clear";
  `gesture`: VisualizationSpatialSelectionGesture;
  `geometry`?: VisualizationSpatialSelectionGeometry;
}

@discriminator("kind")
model VisualizationGeographicLayer {
  `id`: string;
  `kind`: string;
  `label`?: VisualizationFieldRef;
  `tooltip`: VisualizationFieldRef[];
  `position`: VisualizationMapLayerPosition;
  `visibility`: VisualizationMapVisibility;
}

model VisualizationPointLayer extends VisualizationGeographicLayer {
  `kind`: "point";
  `latitude`: VisualizationFieldRef;
  `longitude`: VisualizationFieldRef;
  `value`?: VisualizationFieldRef;
  `category`?: VisualizationFieldRef;
  `size`: VisualizationMapSizeScale;
  `color`: VisualizationMapColorScale;
  `stroke`: VisualizationMapStroke;
  `cluster`: VisualizationMapCluster;
  `opacity`: float64;
}

model VisualizationChoroplethLayer extends VisualizationGeographicLayer {
  `kind`: "choropleth";
  `geometry`: VisualizationGeometryAsset;
  `join`: VisualizationFieldRef;
  `value`?: VisualizationFieldRef;
  `category`?: VisualizationFieldRef;
  `color`: VisualizationMapColorScale;
  `stroke`: VisualizationMapStroke;
  `opacity`: float64;
}

model VisualizationHeatLayer extends VisualizationGeographicLayer {
  `kind`: "heat";
  `latitude`: VisualizationFieldRef;
  `longitude`: VisualizationFieldRef;
  `value`?: VisualizationFieldRef;
  `color`: VisualizationMapColorScale;
  `heat`: VisualizationMapHeatStyle;
  `opacity`: float64;
}

model VisualizationDensityLayer extends VisualizationGeographicLayer {
  `kind`: "density";
  `latitude`: VisualizationFieldRef;
  `longitude`: VisualizationFieldRef;
  `value`?: VisualizationFieldRef;
  `color`: VisualizationMapColorScale;
  `heat`: VisualizationMapHeatStyle;
  `opacity`: float64;
}

model VisualizationReferenceLayer extends VisualizationGeographicLayer {
  `kind`: "reference";
  `geometry`: VisualizationGeometryAsset;
  `color`: VisualizationMapColorScale;
  `stroke`: VisualizationMapStroke;
  `opacity`: float64;
}

model VisualizationPathLayer extends VisualizationGeographicLayer {
  `kind`: "path";
  `latitude`: VisualizationFieldRef;
  `longitude`: VisualizationFieldRef;
  `path`: VisualizationFieldRef;
  `order`: VisualizationFieldRef;
  `value`?: VisualizationFieldRef;
  `category`?: VisualizationFieldRef;
  `color`: VisualizationMapColorScale;
  `stroke`: VisualizationMapStroke;
  `line`: VisualizationMapLineStyle;
  `opacity`: float64;
}

model GeographicVisualizationSpec extends VisualizationSpec {
  `kind`: "geographic";
  `layers`: VisualizationGeographicLayer[];
  `spatialInteractions`: VisualizationSpatialSelectionInteraction[];
  `presentation`: GeographicVisualizationPresentation;
}

model VisualizationInlineDataset {
  `id`: string;
  `specRevision`: string;
  `dataRevision`: int64;
  `generation`: int64;
  `columns`: string[];
  `rows`: unknown[][];
  `completeness`: VisualizationCompleteness;
}

model VisualizationCardinality {
  `kind`: VisualizationCardinalityKind;
  `count`?: int64;
}

model VisualizationWindowBlock {
  `id`: string;
  `start`: int64;
  `rows`: unknown[][];
  `requestSeq`: int64;
  `resetVersion`: int64;
  `sort`: VisualizationSort[];
}

@apigen.contract(#{ kind: "visualization-command", tags: #["visualization", "window"] })
model VisualizationWindowRequest {
  `visualID`: string;
  `specRevision`: string;
  `dataRevision`: int64;
  `requestSeq`: int64;
  `resetVersion`: int64;
  `start`: int64;
  `limit`: int64;
  `sort`: VisualizationSort[];
  `blockID`: string;
}

model VisualizationSpatialBounds {
  `west`: float64;
  `south`: float64;
  `east`: float64;
  `north`: float64;
}

@discriminator("kind")
model VisualizationDataState {
  `kind`: string;
  `specRevision`: string;
  `dataRevision`: int64;
  `generation`: int64;
}

model InlineVisualizationDataState extends VisualizationDataState {
  `kind`: "inline";
  `datasets`: VisualizationInlineDataset[];
}

model WindowedVisualizationDataState extends VisualizationDataState {
  `kind`: "windowed";
  `schema`: VisualizationDatasetSchema;
  `cardinality`: VisualizationCardinality;
  `availableRows`: int64;
  `rowCap`: int64;
  `chunkSize`: int64;
  `resetVersion`: int64;
  `sort`: VisualizationSort[];
  `blocks`: Record<VisualizationWindowBlock>;
}

model VisualizationSpatialScaleDomain {
  `field`: string;
  `minimum`?: float64;
  `maximum`?: float64;
  `total`?: float64;
}

/**
 * Row-free metadata and transport contract for server-generated vector tiles.
 * Tile URLs are opaque, revision-scoped capabilities; this state deliberately
 * contains neither query rows nor authored filter values.
 */
model SpatialTiledVisualizationDataState extends VisualizationDataState {
  `kind`: "spatial_tiled";
  `schema`: VisualizationDatasetSchema;
  `cardinality`: VisualizationCardinality;
  `extent`: VisualizationSpatialBounds;
  `rawDomains`: VisualizationSpatialScaleDomain[];
  `aggregateDomains`: VisualizationSpatialScaleDomain[];
  `tileURL`: string;
  `minimumZoom`: int32;
  `maximumZoom`: int32;
  /**
   * First zoom rendered at raw coordinate grain. A value of maximumZoom + 1
   * means the revision remains aggregate at every supported zoom.
   */
  `rawMinimumZoom`: int32;
  `featureCap`: int64;
  `maximumTileBytes`: int64;
}

/**
 * Versioned transport header for carrying a data state as one opaque browser
 * signal. The payload is decoded only after every repeated revision and
 * discriminator field agrees with this header.
 */
@apigen.contract(#{ kind: "visualization-transport", tags: #["visualization", "data"] })
model VisualizationDataStateTransport {
  `schemaVersion`: 1;
  `encoding`: VisualizationDataStateTransportEncoding;
  `kind`: VisualizationDataStateKind;
  `specRevision`: string;
  `dataRevision`: int64;
  `generation`: int64;
  `payload`: string;
}

model VisualizationDatumRef {
  `dataset`: string;
  `dataRevision`: int64;
  `identity`: Record<unknown>;
  `resetVersion`?: int64;
  `blockID`?: string;
}

model VisualizationSelectionEntry {
  `datum`: VisualizationDatumRef;
  `label`?: string;
}

model VisualizationHighlightMapping {
  `targetFieldID`: string;
  `targetDatasetID`?: string;
  `grain`?: string;
  `value`: unknown;
  `label`?: string;
}

model VisualizationHighlightEntry {
  `mappings`: VisualizationHighlightMapping[];
  `label`: string;
}

model VisualizationHighlightState {
  `sourceVisualID`: string;
  `interactionID`: string;
  `entries`: VisualizationHighlightEntry[];
  `spatialGeometry`?: VisualizationSpatialSelectionGeometry;
  `spatialLatitudeFieldID`?: string;
  `spatialLongitudeFieldID`?: string;
  `label`: string;
}

model VisualizationStatus {
  `kind`: VisualizationStatusKind;
  `message`?: string;
}

model VisualizationDiagnostic {
  `code`: string;
  `severity`: VisualizationDiagnosticSeverity;
  `message`: string;
  `fieldID`?: string;
}

@apigen.contract(#{ kind: "visualization-envelope", tags: #["visualization", "ir"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "visualization-envelope" })
model VisualizationEnvelope {
  `schemaVersion`: 11;
  `visualID`: string;
  `rendererID`: string;
  `specRevision`: string;
  `spec`: VisualizationSpec;
  `dataRevision`: int64;
  `dataState`: VisualizationDataState;
  `selection`: VisualizationSelectionEntry[];
  `highlights`: VisualizationHighlightState[];
  `spatialSelection`?: VisualizationSpatialSelectionState;
  `status`: VisualizationStatus;
  `diagnostics`: VisualizationDiagnostic[];
}
