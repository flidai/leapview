import "@yacobolo/apigen";

@doc("Capability-owned model-facing contracts for LeapView's curated governed read and authoring agent tools.")
@apigen.`package`(#{
  title: "LeapView Agent Tool Contracts",
  version: "1.0.0",
  description: "Compact, model-facing contracts for LeapView's curated governed read and authoring tools.",
})
namespace LeapViewAgentTools;

model QueryFreshness {
  @doc("Completion time of the last successful publish or refresh. This is not an upstream event-time watermark.")
  lastSuccessfulRefreshAt: utcDateTime;
  @doc("DuckLake snapshot ID recorded for the successful data version.")
  snapshotId: string;
  @doc("Serving state that produced the successful data version.")
  servingStateId: string;
  @doc("Whether the data version came from publish or refresh.")
  source: "publish" | "refresh";
  @doc("current when the successful data version belongs to the queried serving snapshot; stale otherwise.")
  status: "current" | "stale";
}

model DashboardVisualColumn {
  id: string;
  sourceRef?: string;
  label: string;
  role: "dimension" | "measure" | "metadata" | "identity";
  dataType: "string" | "boolean" | "integer" | "decimal" | "temporal" | "date" | "geographic";
  nullable: boolean;
  format?: Record<unknown>;
  grain?: string;
}

model DashboardFilterControl {
  type: string;
  operator?: string;
  preset?: string;
  from?: string;
  to?: string;
  value?: string;
  values?: string[];
}

model DashboardAppliedFilters {
  controls: Record<DashboardFilterControl>;
  selections: Record<unknown>[];
  spatialSelections: Record<unknown>[];
}

model DashboardVisualStatus {
  kind: "idle" | "loading" | "ready" | "no_data" | "partial" | "error";
  message?: string;
}

model DashboardVisualDiagnostic {
  code: string;
  severity: "info" | "warning" | "error";
  message: string;
  fieldId?: string;
}

model DashboardVisualCompleteness {
  returnedRows: int32;
  availableRows?: int64;
  cardinality: "unknown" | "lower_bound" | "estimated" | "exact";
  cardinalityCount?: int64;
  state: "complete" | "truncated" | "partial" | "empty";
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["dashboard", "visual", "query"] })
model DashboardVisualQueryResult {
  queryId: string;
  servingSnapshot: string;
  freshness?: QueryFreshness;
  visualId: string;
  title: string;
  type: string;
  mark?: string;
  datasetId: string;
  columns: DashboardVisualColumn[];
  rows: unknown[][];
  appliedFilters: DashboardAppliedFilters;
  status: DashboardVisualStatus;
  diagnostics: DashboardVisualDiagnostic[];
  completeness: DashboardVisualCompleteness;
  hasMore: boolean;
  nextCursor?: string;
}

enum CatalogType {
  workspace: "workspace",
  dashboard: "dashboard",
  page: "page",
  visual: "visual",
  filter: "filter",
  semanticModel: "semantic_model",
  semanticTable: "semantic_table",
  field: "field",
  measure: "measure",
}

model CatalogRef {
  @minLength(1)
  workspaceId: string;
  type: CatalogType;
  @minLength(1)
  id: string;
}

enum QueryVisualType {
  line;
  area;
  bar;
  column;
  pie;
  donut;
  scatter;
  funnel;
  treemap;
  gauge;
  heatmap;
  sankey;
  graph;
  map;
  candlestick;
  boxplot;
  combo;
  waterfall;
  histogram;
  radar;
  tree;
  sunburst;
  kpi;
  table;
  matrix;
  pivot;
}

model QueryVisualFieldRef {
  @doc("Semantic field ID.")
  @minLength(1)
  field: string;
  @doc("Optional output alias.")
  `alias`?: string;
}

model QueryVisualSort {
  @doc("Semantic field ID.")
  @minLength(1)
  field: string;
  @doc("Sort direction.")
  direction?: "asc" | "desc";
}

model QueryVisualFilterGroupInput {
  @doc("Nested governed filters. The next recursive level is validated by the tool provider.")
  filters: Record<unknown>[];
}

model QueryVisualFilterInput {
  @doc("Semantic field ID. Omit only when groups supplies nested filters.")
  field?: string;
  @doc("Fact table used to resolve an otherwise ambiguous conformed dimension.")
  fact?: string;
  @doc("Governed comparison operator. Defaults to equals.")
  operator?: "equals" | "in" | "contains" | "not_contains" | "starts_with" | "greater_than_or_equal" | "less_than" | "is_null" | "is_not_null";
  @doc("Comparison values. is_null and is_not_null accept no values.")
  values?: string[];
  @doc("Nested filter groups.")
  groups?: QueryVisualFilterGroupInput[];
}

model QueryVisualTableFormattingRule {
  kind: string;
  values?: Record<string>;
  min?: float64;
  max?: float64;
  color?: string;
  background?: string;
  lowColor?: string;
  highColor?: string;
}

model QueryVisualTableColumn {
  key: string;
  label: string;
  align?: string;
  role?: string;
  group?: string;
  measure?: string;
  columnValue?: string;
  width?: int32;
  format?: string;
  formatting?: QueryVisualTableFormattingRule[];
}

model QueryVisualThreshold {
  value: float64;
  tone: string;
}

model QueryVisualLabelPolicy {
  density?: "hidden" | "automatic" | "dense" | "always";
  priority?: ("selected" | "anomaly" | "threshold")[];
  @minValue(4)
  @maxValue(200)
  maxCharacters?: int32;
  @minValue(0)
  @maxValue(64)
  minimumSpacing?: int32;
  tooltipFallback?: boolean;
}

model QueryVisualPresentation {
  legend?: "none" | "top" | "right" | "bottom" | "left";
  showLabels?: boolean;
  labels?: QueryVisualLabelPolicy;
  stacked?: boolean;
  smooth?: boolean;
  showSymbols?: boolean;
  dataZoom?: boolean;
  area?: boolean;
  step?: boolean;
  orientation?: "horizontal" | "vertical";
  labelPosition?: "inside" | "outside" | "top" | "right" | "bottom" | "left" | "center";
  @minValue(0)
  symbolSize?: float64;
  @minValue(5)
  @maxValue(60)
  histogramBins?: int32;
  seriesTypes?: Record<"line" | "area" | "bar" | "column">;
  dualAxis?: boolean;
  rose?: boolean;
  centerLabel?: string;
  @minValue(0)
  innerRadius?: float64;
  @minValue(0)
  outerRadius?: float64;
  align?: string;
  sort?: string;
  @minValue(0)
  initialDepth?: int32;
  basemap?: string;
  roam?: boolean;
  layout?: string;
  breadcrumb?: boolean;
  @minValue(0)
  nodeGap?: float64;
  @minValue(0)
  @maxValue(1)
  curveness?: float64;
  focus?: string;
  minimum?: float64;
  maximum?: float64;
  @minValue(0)
  progressWidth?: float64;
  thresholds?: QueryVisualThreshold[];
  note?: string;
  tone?: string;
}

model QueryVisualCalculationOrder {
  field: string;
  direction?: "asc" | "desc";
}

model QueryVisualCalculationLookup {
  field: string;
  value: string;
}

model QueryVisualCalculation {
  id: string;
  label?: string;
  template: "running_total" | "moving_average" | "difference" | "percentage_difference" | "percent_of_parent" | "percent_of_grand_total" | "rank" | "cumulative_contribution" | "lookup";
  source: string;
  axis?: "rows" | "columns" | "hierarchy" | "facets";
  orderBy?: QueryVisualCalculationOrder[];
  partitionBy?: string[];
  reset?: "none" | "highest_parent" | "lowest_parent";
  @minValue(1)
  window?: int32;
  @minValue(1)
  offset?: int32;
  parent?: string;
  lookup?: QueryVisualCalculationLookup;
  hidden?: boolean;
  format?: "number" | "decimal" | "integer" | "percent" | "compact" | "currency";
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["semantic-model", "visual", "query"] })
model QueryVisualInput {
  @minLength(1)
  workspace: string;
  @doc("Semantic model ID.")
  @minLength(1)
  `model`: string;
  @doc("Semantic dataset/table ID.")
  @minLength(1)
  dataset: string;
  @doc("Optional display title.")
  title?: string;
  @doc("Visual type.")
  type: QueryVisualType;
  @doc("Dimension fields for chart grouping.")
  @maxItems(50)
  dimensions?: QueryVisualFieldRef[];
  @doc("Optional series field for split charts.")
  series?: QueryVisualFieldRef;
  @doc("Measure fields for chart values.")
  @maxItems(50)
  measures?: QueryVisualFieldRef[];
  @doc("Fields for table output.")
  @maxItems(50)
  fields?: QueryVisualFieldRef[];
  @doc("Row fields for matrix-style table output.")
  @maxItems(50)
  rows?: QueryVisualFieldRef[];
  @doc("Optional table column display configuration.")
  @maxItems(50)
  columns?: QueryVisualTableColumn[];
  @doc("Governed semantic filters applied before visualization. Inline data and arbitrary expressions are not accepted.")
  @maxItems(100)
  filters?: QueryVisualFilterInput[];
  @doc("Sort fields applied to the query result.")
  @maxItems(50)
  sort?: QueryVisualSort[];
  @doc("Maximum result rows.")
  @minValue(1)
  @maxValue(50)
  limit?: int32;
  @doc("Typed renderer-independent presentation settings.")
  presentation?: QueryVisualPresentation;
  @doc("Closed post-aggregation calculations over compiled result-frame aliases.")
  @maxItems(20)
  calculations?: QueryVisualCalculation[];
}

model QueryVisualFieldUsage {
  ref: CatalogRef;
  role: "dimension" | "series" | "measure" | "table_field" | "table_row";
  `alias`?: string;
  label: string;
  dataType?: string;
  unit?: string;
  format?: string;
}

model QueryVisualFilterUsage {
  operator: "equals" | "in" | "contains" | "not_contains" | "starts_with" | "greater_than_or_equal" | "less_than" | "is_null" | "is_not_null";
  @doc("Filtered semantic field.")
  ref: CatalogRef;
  @doc("Fact table selected to resolve a conformed dimension.")
  resolvedFactRef?: CatalogRef;
  @doc("Comparison values. Omitted for unary operators.")
  values?: string[];
  @doc("Zero-based nested group path. Omitted for a root-level filter.")
  path?: int32[];
}

model QueryVisualCompleteness {
  returnedRows: int32;
  limit: int32;
  status: "complete" | "limit_reached" | "empty";
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["semantic-model", "visual", "query"] })
model QueryVisualResult {
  ok: true;
  queryId: string;
  servingSnapshot: string;
  freshness?: QueryFreshness;
  type: string;
  id: string;
  title: string;
  modelRef: CatalogRef;
  datasetRef: CatalogRef;
  fields: QueryVisualFieldUsage[];
  filters: QueryVisualFilterUsage[];
  completeness: QueryVisualCompleteness;
  status: DashboardVisualStatus;
  diagnostics: DashboardVisualDiagnostic[];
  summary: string;
  signal: string;
}

model CatalogLocation {
  @minLength(1)
  dashboardId: string;
  dashboardName?: string;
  @minLength(1)
  pageId: string;
  pageName?: string;
  href?: string;
}

model CatalogLocationSelection {
  @minLength(1)
  dashboardId: string;
  @minLength(1)
  pageId: string;
}

model CatalogHierarchyItem {
  ref: CatalogRef;
  name: string;
}

model CatalogWorkspace {
  ref: CatalogRef;
  name: string;
}

model CatalogItem {
  ref: CatalogRef;
  name: string;
  description?: string;
  workspace: CatalogWorkspace;
  hierarchy: CatalogHierarchyItem[];
  locations?: CatalogLocation[];
  href?: string;
  capabilities: ("catalog_get" | "catalog_list" | "query_semantic_model" | "query_dashboard_visual" | "query_visual")[];
}

model CatalogSearchContext {
  dashboardId?: string;
  pageId?: string;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["catalog", "search"] })
model CatalogSearchInput {
  @minLength(1)
  query: string;
  types?: CatalogType[];
  workspaceIds?: string[];
  context?: CatalogSearchContext;
  cursor?: string;
  @minValue(1)
  @maxValue(25)
  limit?: int32;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["catalog", "browse"] })
model CatalogListInput {
  parent?: CatalogRef;
  childTypes?: CatalogType[];
  cursor?: string;
  @minValue(1)
  @maxValue(50)
  limit?: int32;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["catalog", "describe"] })
model CatalogGetInput {
  ref: CatalogRef;
  location?: CatalogLocationSelection;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["catalog"] })
model CatalogPage {
  items: CatalogItem[];
  @minValue(0)
  count: int32;
  hasMore: boolean;
  nextCursor?: string;
}

model CatalogPlacement {
  col?: int32;
  row?: int32;
  colSpan?: int32;
  rowSpan?: int32;
  x?: float64;
  y?: float64;
  width?: float64;
  height?: float64;
}

model CatalogColumn {
  key: string;
  label: string;
  role?: string;
  format?: string;
}

model CatalogPageComponent {
  id: string;
  kind: string;
  ref?: string;
  title?: string;
  description?: string;
  placement?: CatalogPlacement;
  x?: float64;
  y?: float64;
  width?: float64;
  height?: float64;
  visualId?: string;
  filterId?: string;
}

model CatalogRelationship {
  id: string;
  description: string;
  fromFieldRef: CatalogRef;
  toFieldRef: CatalogRef;
  cardinality: string;
  active: boolean;
}

model CatalogFieldBinding {
  semanticTableRef: CatalogRef;
  fieldRef: CatalogRef;
  relationshipPath: string[];
}

model CatalogMeasureInput {
  fieldRef?: CatalogRef;
  expression?: string;
}

model CatalogMeasureFilter {
  fieldRef: CatalogRef;
  operator: string;
  values: unknown[];
}

@discriminator("type")
model CatalogDetails {
  type: string;
}

model CatalogWorkspaceDetails extends CatalogDetails {
  type: "workspace";
  activeServingStateId: string;
}

model CatalogDashboardDetails extends CatalogDetails {
  type: "dashboard";
  semanticModelRef: CatalogRef;
  pageCount: int32;
  visualCount: int32;
  filterCount: int32;
}

model CatalogPageDetails extends CatalogDetails {
  type: "page";
  components: CatalogPageComponent[];
}

model CatalogVisualDetails extends CatalogDetails {
  type: "visual";
  visualType: string;
  shape: string;
  renderer: string;
  query: Record<unknown>;
  columns: CatalogColumn[];
  placement: CatalogPlacement;
}

model CatalogFilterDetails extends CatalogDetails {
  type: "filter";
  field: string;
  configuration: Record<unknown>;
  placement: CatalogPlacement;
}

model CatalogSemanticModelDetails extends CatalogDetails {
  type: "semantic_model";
  semanticTableCount: int32;
  fieldCount: int32;
  conformedDimensionCount: int32;
  atomicMeasureCount: int32;
  metricCount: int32;
  factCount: int32;
  relationshipCount: int32;
  relationships: CatalogRelationship[];
  dashboardCount: int32;
  dashboardUsage: CatalogRef[];
}

model CatalogSemanticTableDetails extends CatalogDetails {
  type: "semantic_table";
  source: string;
  sources: string[];
  grain: string;
  primaryKey: string;
  keys: string[];
  roles: ("fact" | "dimension")[];
  fieldCount: int32;
  measureCount: int32;
}

model CatalogFieldDetails extends CatalogDetails {
  type: "field";
  kind: string;
  table?: string;
  label: string;
  dataType: string;
  grain?: string;
  timeGrains?: string[];
  expression?: string;
  sourceField?: string;
  nullable?: boolean;
  primaryKey?: boolean;
  bindings?: CatalogFieldBinding[];
}

model CatalogMeasureDetails extends CatalogDetails {
  type: "measure";
  kind: string;
  table?: string;
  label: string;
  aggregation?: string;
  factRef?: CatalogRef;
  input?: CatalogMeasureInput;
  filters?: CatalogMeasureFilter[];
  empty?: string;
  expression?: string;
  dependencyRefs: CatalogRef[];
  unit: string;
  format: string;
  hidden: boolean;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["catalog", "describe"] })
model CatalogGetResult {
  item: CatalogItem;
  details: CatalogDetails;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["documentation", "search"] })
model DocsSearchInput {
  @doc("Words or phrases to find in LeapView documentation.")
  @minLength(1)
  query: string;
  @doc("Optional documentation path prefix, such as guides/build, api, cli, or visuals.")
  path?: string;
  @doc("Opaque nextCursor from a previous call with the same query and path.")
  @minLength(1)
  cursor?: string;
  @doc("Maximum matches to return; defaults to 8.")
  @minValue(1)
  @maxValue(25)
  limit?: int32;
}

model DocsReference {
  id: string;
  path: string;
  title: string;
  summary: string;
  url: string;
  excerpt: string;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["documentation", "search"] })
model DocsSearchResult {
  query: string;
  path?: string;
  matches: DocsReference[];
  @minValue(0)
  count: int32;
  hasMore: boolean;
  nextCursor?: string;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["documentation", "read"] })
model DocsReadInput {
  @doc("Stable doc:<path> ID returned by docs_search.")
  @minLength(1)
  id: string;
  @doc("First line to read, starting at 1; defaults to 1.")
  @minValue(1)
  offset?: int32;
  @doc("Maximum lines to read; defaults to 200 and is also byte-bounded.")
  @minValue(1)
  @maxValue(500)
  limit?: int32;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["documentation", "read"] })
model DocsReadResult {
  id: string;
  path: string;
  title: string;
  url: string;
  content: string;
  lineStart: int32;
  lineEnd: int32;
  totalLines: int32;
  nextOffset?: int32;
  truncated: boolean;
}

// Dashboard authoring tools deliberately keep the model-facing surface
// narrow. Authenticated actor and provenance fields are server-bound and are
// therefore not accepted by any input contract. The closed authoring.Command
// union remains the validation authority for command payloads.
model DashboardAuthoringRevisionToken {
  revisionId: string;
  number: int64;
  contentHash: string;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "catalog"] })
model DashboardAuthoringListInput {
  @minLength(1)
  workspace: string;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["dashboard", "authoring", "catalog"] })
model DashboardAuthoringListResult {
  items: Record<unknown>[];
  count: int32;
  projectCount: int32;
  workspaceCount: int32;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "catalog"] })
model DashboardAuthoringGetInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboard: string;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["dashboard", "authoring", "catalog"] })
model DashboardAuthoringGetResult {
  dashboard: Record<unknown>;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "draft"] })
model DashboardAuthoringDraftGetInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboard: string;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["dashboard", "authoring", "draft"] })
model DashboardAuthoringDraftGetResult {
  lifecycle: Record<unknown>;
  revision: Record<unknown>;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "create"] })
model DashboardAuthoringCreateInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  title: string;
  @minLength(1)
  semanticModel: string;
  @minLength(1)
  dashboardId?: string;
  @minLength(1)
  slug?: string;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["dashboard", "authoring"] })
model DashboardAuthoringResult {
  revision: Record<unknown>;
  lifecycle: Record<unknown>;
}

model AuthoringPublish {}
model AuthoringArchive {}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "intent"] })
model DashboardAuthoringSetVisibilityInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboardId: string;
  @minLength(1)
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  visibility: "private" | "shared";
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "intent"] })
model DashboardAuthoringAddPageInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboardId: string;
  @minLength(1)
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  pageId?: string;
  title?: string;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "intent"] })
model DashboardAuthoringAddVisualInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboardId: string;
  @minLength(1)
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  @minLength(1)
  pageId: string;
  visualId?: string;
  componentId?: string;
  @minLength(1)
  type: string;
  title?: string;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "intent"] })
model DashboardAuthoringAssignFieldInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboardId: string;
  @minLength(1)
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  @minLength(1)
  pageId: string;
  @minLength(1)
  visualId: string;
  @minLength(1)
  fieldId: string;
  role: "measure" | "dimension" | "detail";
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "command"] })
model DashboardAuthoringCommandInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboardId: string;
  @minLength(1)
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  @minLength(1)
  contentHash?: string;
  publish?: AuthoringPublish;
  archive?: AuthoringArchive;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "fork"] })
model DashboardAuthoringForkInput {
  sourceKind: "workspace" | "project";
  @minLength(1)
  sourceWorkspace: string;
  @minLength(1)
  sourceDashboard: string;
  @minLength(1)
  targetWorkspace?: string;
  @minLength(1)
  title?: string;
  @minLength(1)
  slug?: string;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "preview"] })
model DashboardAuthoringPreviewInput {
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboard: string;
  @minLength(1)
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  @minLength(1)
  page: string;
  filters?: Record<unknown>;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["dashboard", "authoring", "preview"] })
model DashboardAuthoringPreviewResult {
  revision: Record<unknown>;
  definition: Record<unknown>;
  pagePatch: Record<unknown>;
  semanticEvidence: Record<unknown>;
}

@apigen.contract(#{ kind: "agent-tool-input", tags: #["dashboard", "authoring", "export"] })
model DashboardAuthoringExportInput {
  sourceKind: "workspace" | "project";
  @minLength(1)
  workspace: string;
  @minLength(1)
  dashboard: string;
}

@apigen.contract(#{ kind: "agent-tool-output", tags: #["dashboard", "authoring", "export"] })
model DashboardAuthoringExportResult {
  yaml: string;
}
