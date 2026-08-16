using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Dashboard;

model DashboardSummary {
  id: ResourceId;
  title: string;
  description: string;
  semanticModel: ResourceId;
  tags: string[];
  pageCount: int32;
}

model DashboardListResponse {
  items: DashboardSummary[];
  page: PageInfo;
}

model DashboardAppearancePatchRequest {
  icon?: string;
  color?: string;
}

model DashboardAppearanceResponse {
  icon: string;
  color: string;
  revision: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "standard" })
model DashboardAppearanceUpdatedAuditPayload {
  @apigen.auditPublic
  fields: string[];
}

model SemanticModelSummary {
  id: ResourceId;
  title: string;
  description: string;
}

model SemanticModelListResponse {
  items: SemanticModelSummary[];
  page: PageInfo;
}

model ModelRef {
  id: ResourceId;
  title: string;
}

model DashboardManifestCounts {
  pages: int32;
  visuals: int32;
  filters: int32;
}

model DashboardManifestComponent {
  id: string;
  kind: "visual" | "slicer" | "header";
  ref: string;
  title?: string;
}

model DashboardComponentPlacement {
  col?: int32;
  row?: int32;
  colSpan?: int32;
  rowSpan?: int32;
}

@discriminator("kind")
model DashboardComponentResponse {
  id: string;
  kind: "visual" | "slicer" | "header";
  ref?: string;
  title?: string;
  description?: string;
  placement?: DashboardComponentPlacement;
  x?: float64;
  y?: float64;
  width?: float64;
  height?: float64;
}

model DashboardVisualComponentResponse extends DashboardComponentResponse {
  kind: "visual";
  visualId: string;
}

model DashboardSlicerComponentResponse extends DashboardComponentResponse {
  kind: "slicer";
  filterId: string;
}

model DashboardHeaderComponentResponse extends DashboardComponentResponse {
  kind: "header";
}

model DashboardPageResponse {
  id: string;
  title: string;
  description?: string;
  components: DashboardComponentResponse[];
}

model DashboardVisualDescribeResponse {
  id: string;
  componentId?: string;
  rendererID: string;
  specRevision: string;
  spec: LeapViewVisualization.VisualizationSpec;
  placement?: DashboardComponentPlacement;
  x?: float64;
  y?: float64;
  width?: float64;
  height?: float64;
}

model DashboardFilterPredicatePolicy {
  kind: "null_check" | "set" | "comparison" | "range" | "relative_period";
  operators: string[];
}

model DashboardFilterStaticOption {
  value: DashboardFilterValue;
  label: string;
}

model DashboardFilterOptionSource {
  kind: "none" | "static" | "distinct";
  limit: int32;
  values: DashboardFilterStaticOption[];
}

model DashboardCompiledFilterDefinition {
  id: string;
  label: string;
  description?: string;
  field: string;
  fact?: string;
  valueKind: "string" | "boolean" | "integer" | "decimal" | "date" | "timestamp";
  predicates: DashboardFilterPredicatePolicy[];
  options: DashboardFilterOptionSource;
  formatPattern?: string;
  formatUnit?: string;
  timezone: string;
  calendar: string;
  weekStart: string;
}

model DashboardFilterBindingRef {
  scope: "report" | "page";
  id: string;
}

model DashboardCompiledFilterBinding {
  key: string;
  id: string;
  filter: string;
  scope: "report" | "page";
  pageID?: string;
  default: DashboardFilterExpression;
  selectionMode: "single" | "multiple";
  maxSelectedValues: int32;
  readerEditable: boolean;
  urlParam?: string;
  urlEncoding?: "typed_v1";
  paneVisible: boolean;
  paneOrder: int32;
  paneLabel?: string;
  targets: string[];
  optionDependencies: DashboardFilterBindingRef[];
}

model DashboardFilterPresentation {
  style: "dropdown" | "list" | "buttons" | "input" | "numeric_range" | "date_range" | "relative_period";
  search: boolean;
  selectAll: boolean;
  showCounts: boolean;
  showSummary: boolean;
  compact: boolean;
  title?: string;
  description?: string;
  ariaLabel?: string;
}

model DashboardFilterDescribeResponse {
  definition: DashboardCompiledFilterDefinition;
  binding: DashboardCompiledFilterBinding;
  componentId?: string;
  presentation?: DashboardFilterPresentation;
  placement?: DashboardComponentPlacement;
}

model DashboardManifestPage {
  id: string;
  title: string;
  description?: string;
  components: DashboardManifestComponent[];
}

model DashboardManifestResponse {
  id: ResourceId;
  title: string;
  description?: string;
  `semantic_model`?: ResourceId;
  `model`?: ModelRef;
  counts: DashboardManifestCounts;
  pages: DashboardManifestPage[];
  `detail_tools`: Record<string>;
}

model SemanticModelCounts {
  sources: int32;
  `model_tables`: int32;
  fields: int32;
  facts: int32;
  `conformed_dimensions`: int32;
  `atomic_measures`: int32;
  metrics: int32;
  relationships: int32;
}

model SemanticModelTableSummary {
  id: string;
  roles: string[];
  source: string;
  description: string;
  fields: int32;
}

model SemanticDatasetSummary {
  id: string;
  kind: string;
  source: string;
  description: string;
  fieldCount: int32;
  measureCount: int32;
}

model SemanticDatasetListResponse {
  items: SemanticDatasetSummary[];
  page: PageInfo;
}

model SemanticDatasetResponse {
  id: string;
  kind: string;
  source: string;
  sources: string[];
  description: string;
  primaryKey: string;
  grain: string;
  fieldCount: int32;
  measureCount: int32;
}

enum SemanticFieldKind {
  dimension;
  measure;
  metric;
}

model SemanticFieldResponse {
  id: string;
  kind: SemanticFieldKind;
  table: string;
  name: string;
  label: string;
  description?: string;
  type?: string;
  unit?: string;
  format?: string;
  grain?: string;
  time?: string;
  grains?: string[];
}

model SemanticFieldListResponse {
  items: SemanticFieldResponse[];
  page: PageInfo;
}

model SemanticRelationshipResponse {
  id: string;
  fromDataset: string;
  fromField: string;
  toDataset: string;
  toField: string;
  cardinality: string;
  active: boolean;
}

model SemanticRelationshipListResponse {
  items: SemanticRelationshipResponse[];
  page: PageInfo;
}

model SemanticSourceResponse {
  id: string;
  kind: string;
  connection?: string;
  table?: string;
  path?: string;
  description?: string;
}

model SemanticSourceListResponse {
  items: SemanticSourceResponse[];
  page: PageInfo;
}

model SemanticFieldRef {
  @doc("Semantic field or measure ID. Use the ID returned by catalog_list or catalog_get.")
  field: string;
  @doc("Optional output column name.")
  `alias`?: string;
}

enum SemanticTimeGrain {
  day;
  week;
  month;
  quarter;
  year;
}

model SemanticTimeRef {
  @doc("Date or timestamp field ID.")
  field: string;
  @doc("Calendar grain used to group the time field.")
  grain?: SemanticTimeGrain;
  @doc("Optional output column name.")
  `alias`?: string;
}

enum SemanticSortDirection {
  asc;
  desc;
}

model SemanticSort {
  @doc("Selected output field or alias to sort.")
  field: string;
  @doc("Sort direction. Defaults to ascending when omitted.")
  direction?: SemanticSortDirection;
}

model SemanticFilterGroup {
  @doc("Filters combined within this nested group.")
  filters: SemanticFilter[];
}

enum SemanticFilterOperator {
  equals;
  in;
  contains;
  not_contains;
  starts_with;
  greater_than_or_equal;
  less_than;
  is_null;
  is_not_null;
}

model SemanticFilter {
  @doc("Semantic field ID. Omit only when groups supplies nested filters.")
  field?: string;
  @doc("Fact table used to resolve a conformed dimension when it is otherwise ambiguous.")
  fact?: string;
  @doc("Governed comparison operator. equals is used when omitted.")
  operator?: SemanticFilterOperator;
  @doc("Comparison values. is_null and is_not_null accept no values.")
  values?: string[];
  @doc("Nested filter groups.")
  groups?: SemanticFilterGroup[];
}

model SemanticQueryRequest {
  @maxItems(50)
  dimensions?: SemanticFieldRef[];
  @maxItems(50)
  measures?: SemanticFieldRef[];
  time?: SemanticTimeRef;
  @maxItems(100)
  filters?: SemanticFilter[];
  @maxItems(50)
  sort?: SemanticSort[];
  limit?: QueryLimit;
  pageToken?: PageToken;
}

model SemanticPreviewRequest {
  @maxItems(50)
  dimensions?: SemanticFieldRef[];
  @maxItems(50)
  measures?: SemanticFieldRef[];
  @maxItems(100)
  filters?: SemanticFilter[];
  @maxItems(50)
  sort?: SemanticSort[];
  limit?: QueryLimit;
  pageToken?: PageToken;
}

enum QueryColumnType {
  boolean;
  int64;
  decimal;
  float64;
  string;
  date;
  timestamp;
  json;
}

model QueryColumn {
  @doc("Output column name used to match positional row cells.")
  name: string;
  @doc("Precision-safe wire data type. Agent tools expose this property as dataType.")
  type: QueryColumnType;
  @doc("Whether the governed semantic definition can produce SQL NULL.")
  nullable: boolean;
  @doc("Governed catalog ref for the selected field, measure, or metric.")
  fieldRef?: QueryFieldRef;
  @doc("Business-facing semantic label.")
  label?: string;
  @doc("Semantic role of the selected member.")
  kind?: "dimension" | "measure" | "metric";
  @doc("Business unit declared by the semantic measure or metric.")
  unit?: string;
  @doc("Formatting contract declared by the semantic measure or metric.")
  format?: string;
}

model QueryFieldRef {
  @doc("Catalog type of the semantic member.")
  type: "field" | "measure";
  @doc("Stable project-graph ID of the semantic member.")
  id: ResourceId;
}

// Cells are encoded as strings so int64 and decimal values never lose precision.
// The typed column descriptor determines how clients interpret each position;
// SQL NULL is encoded as JSON null and remains distinct from a genuine empty string.
alias SemanticQueryRow = unknown[];

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

model QueryCompleteness {
  @doc("Number of rows present in this response page.")
  returnedRows: int32;
  @doc("Whether another governed page is available.")
  hasMore: boolean;
}

model SemanticQueryResponse {
  @doc("Stable identifier for this query execution.")
  queryId: string;
  @doc("Immutable serving state used by the query and its continuation cursor.")
  servingSnapshot: string;
  @doc("Last successful data-version metadata when the model has a recorded publish or refresh.")
  freshness?: QueryFreshness;
  @doc("Completeness of the exact row page returned.")
  completeness: QueryCompleteness;
  columns: QueryColumn[];
  rows: SemanticQueryRow[];
  page: PageInfo;
}

model SemanticExplainResponse {
  mode: string;
  facts: string[];
  stitchDimensions: string[];
  physicalDependencies: string[];
  relationshipPaths: string[];
  sql: string;
  args: Record<unknown>[];
  columns: string[];
  warnings: string[];
  @doc("Total ordering applied to the result, including deterministic tie-breakers.")
  effectiveOrdering: SemanticSort[];
}

model ModelDashboardUsage {
  id: string;
  title: string;
  `semantic_model`: ResourceId;
  pages: int32;
}

model SemanticModelDescriptionResponse {
  id: ResourceId;
  title: string;
  description: string;
  dashboards: ModelDashboardUsage[];
  counts?: SemanticModelCounts;
  tables?: SemanticModelTableSummary[];
}

@discriminator("kind")
model DashboardFilterValue {
  kind: string;
}

model DashboardStringFilterValue extends DashboardFilterValue {
  kind: "string";
  value: string;
}

model DashboardBooleanFilterValue extends DashboardFilterValue {
  kind: "boolean";
  value: boolean;
}

model DashboardIntegerFilterValue extends DashboardFilterValue {
  kind: "integer";
  value: string;
}

model DashboardDecimalFilterValue extends DashboardFilterValue {
  kind: "decimal";
  value: string;
}

model DashboardDateFilterValue extends DashboardFilterValue {
  kind: "date";
  value: string;
}

model DashboardTimestampFilterValue extends DashboardFilterValue {
  kind: "timestamp";
  value: string;
}

model DashboardFilterBound {
  value: DashboardFilterValue;
  inclusive: boolean;
}

@discriminator("kind")
model DashboardFilterExpression {
  kind: string;
}

model DashboardUnfilteredExpression extends DashboardFilterExpression {
  kind: "unfiltered";
}

model DashboardNullCheckExpression extends DashboardFilterExpression {
  kind: "null_check";
  operator: "is_null" | "is_not_null";
}

model DashboardSetExpression extends DashboardFilterExpression {
  kind: "set";
  operator: "in" | "not_in";
  values: DashboardFilterValue[];
}

model DashboardComparisonExpression extends DashboardFilterExpression {
  kind: "comparison";
  operator: "equals" | "not_equals" | "contains" | "not_contains" | "starts_with" | "ends_with" | "greater_than" | "greater_than_or_equal" | "less_than" | "less_than_or_equal";
  value: DashboardFilterValue;
}

model DashboardRangeExpression extends DashboardFilterExpression {
  kind: "range";
  lower?: DashboardFilterBound;
  upper?: DashboardFilterBound;
}

model DashboardRelativePeriodExpression extends DashboardFilterExpression {
  kind: "relative_period";
  direction: "previous" | "current" | "next";
  count: int32;
  unit: "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year";
  includeCurrent: boolean;
  anchor: "current_time" | "first_available" | "last_available" | "fixed";
  anchorValue?: DashboardFilterValue;
}

model DashboardAppliedFilterInput {
  version: "typed_v1";
  controls: Record<DashboardFilterExpression>;
}

model DashboardSpatialInteractionSelection {
  visualID: string;
  interactionID: string;
  gesture: LeapViewVisualization.VisualizationSpatialSelectionGesture;
  geometry: DashboardSpatialSelectionGeometry;
  order: int32;
}

model DashboardSpatialSelectionGeometry {
  kind: "box" | "lasso" | "radius";
  bounds?: LeapViewVisualization.VisualizationSpatialBounds;
  points?: LeapViewVisualization.VisualizationSpatialCoordinate[];
  center?: LeapViewVisualization.VisualizationSpatialCoordinate;
  radiusMeters?: float64;
}

model DashboardPageQueryRequest {
  filterState?: DashboardAppliedFilterInput;
  interactionSelections?: Record<unknown>[];
  spatialSelections?: DashboardSpatialInteractionSelection[];
}

model DashboardVisualQueryRequest {
  limit?: QueryLimit;
  pageToken?: PageToken;
  filterState?: DashboardAppliedFilterInput;
  interactionSelections?: Record<unknown>[];
  spatialSelections?: DashboardSpatialInteractionSelection[];
}

model DashboardPageQueryResponse {
  filterState: DashboardAppliedFilterInput;
  status?: Record<unknown>;
  visuals: Record<LeapViewVisualization.VisualizationEnvelope>;
}

model DashboardTableColumn {
  key: string;
  label: string;
  role?: string;
  format?: string;
}

model DashboardTableQueryResponse {
  type: "table" | "matrix" | "pivot";
  queryId: string;
  servingSnapshot: string;
  title: string;
  columns: QueryColumn[];
  rows: string[][];
  availableRows: int32;
  page: PageInfo;
}

model DashboardFilterOptionResponse {
  value: string;
  label: string;
}

model DashboardFilterOptionListResponse {
  items: DashboardFilterOptionResponse[];
  page: PageInfo;
}

@tag("BI")
@summary("List dashboards")
@route("/dashboards")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@apigen.cli(#{ command: #["dashboards", "list"], output: #{ mode: "raw" } })
op listDashboards(@query limit?: ListLimit, @query pageToken?: PageToken): OkJson<DashboardListResponse> | CommonErrors;

@tag("BI")
@summary("Describe dashboard")
@route("/dashboards/{dashboard}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "dashboard")
@apigen.cli(#{ command: #["dashboards", "describe"], args: #[#{ source: "path", name: "dashboard", displayName: "dashboard-id" }], output: #{ mode: "raw" } })
op getDashboard(@path dashboard: ResourceId): OkJson<DashboardManifestResponse> | CommonErrors;

@tag("BI")
@summary("Describe dashboard page")
@route("/dashboards/{dashboard}/pages/{page}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "dashboard")
@apigen.cli(#{ command: #["dashboards", "page"], args: #[#{ source: "path", name: "dashboard", displayName: "dashboard-id" }, #{ source: "path", name: "page", displayName: "page" }], output: #{ mode: "raw" } })
op getDashboardPage(@path dashboard: ResourceId, @path page: string): OkJson<DashboardPageResponse> | CommonErrors;

@tag("BI")
@summary("Describe dashboard visual")
@route("/dashboards/{dashboard}/pages/{page}/visuals/{visual}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "dashboard")
@apigen.cli(#{ command: #["dashboards", "visual"], args: #[#{ source: "path", name: "dashboard", displayName: "dashboard-id" }, #{ source: "path", name: "page", displayName: "page" }, #{ source: "path", name: "visual", displayName: "visual" }], output: #{ mode: "raw" } })
op getDashboardVisual(@path dashboard: ResourceId, @path page: string, @path visual: string): OkJson<DashboardVisualDescribeResponse> | CommonErrors;

@tag("BI")
@summary("Describe dashboard filter")
@route("/dashboards/{dashboard}/pages/{page}/filters/{filter}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "dashboard")
op getDashboardFilter(@path dashboard: ResourceId, @path page: string, @path filter: string): OkJson<DashboardFilterDescribeResponse> | CommonErrors;

@tag("BI")
@summary("List semantic models")
@route("/semantic-models")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@apigen.cli(#{ command: #["semantic-models", "list"], output: #{ mode: "raw" } })
op listSemanticModels(@query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticModelListResponse> | CommonErrors;

@tag("BI")
@summary("Describe semantic model")
@route("/semantic-models/{model}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "semantic-model")
@apigen.cli(#{ command: #["semantic-models", "describe"], args: #[#{ source: "path", name: "model", displayName: "model-id" }], output: #{ mode: "raw" } })
op getSemanticModel(@path("model") modelName: ResourceId): OkJson<SemanticModelDescriptionResponse> | CommonErrors;

@tag("BI")
@summary("List semantic model relationships")
@route("/semantic-models/{model}/relationships")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "semantic-model")
op listSemanticRelationships(@path("model") modelName: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticRelationshipListResponse> | CommonErrors;

@tag("BI")
@summary("List semantic model sources")
@route("/semantic-models/{model}/sources")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "semantic-model")
op listSemanticSources(@path("model") modelName: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticSourceListResponse> | CommonErrors;

@tag("BI")
@summary("List semantic model fields")
@route("/semantic-models/{model}/fields")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "semantic-model")
@apigen.cli(#{ command: #["semantic-models", "model-fields"], args: #[#{ source: "path", name: "model", displayName: "model-id" }], output: #{ mode: "raw" } })
op listSemanticModelFields(@path("model") modelName: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticFieldListResponse> | CommonErrors;

@tag("BI")
@summary("Query governed semantic data with typed columns, filters, pagination, and provenance")
@route("/semantic-models/{model}/query")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
@extension("x-leapview-object-scope", "semantic-model")
@extension("x-leapview-response-trailers", #["X-Next-Cursor"])
@apigen.cli(#{ command: #["semantic-models", "model-query"], args: #[#{ source: "path", name: "model", displayName: "model-id" }], output: #{ mode: "raw" } })
@apigen.tool(#{
  name: "query_semantic_model", effect: "read", tags: #["semantic-model", "query"],
  input: #{ fields: #[#{ source: "path", name: "model", mode: "model", description: "Stable semantic-model resource ID to query." }, #{ source: "body", name: "limit", default: 25 }] },
  output: #{
    mode: "project",
    select: #[
      #{ source: "/queryId" },
      #{ source: "/servingSnapshot" },
      #{ source: "/freshness", select: #[
        #{ source: "/lastSuccessfulRefreshAt" },
        #{ source: "/snapshotId" },
        #{ source: "/servingStateId" },
        #{ source: "/source" },
        #{ source: "/status" }
      ] },
      #{ source: "/completeness", select: #[
        #{ source: "/returnedRows" },
        #{ source: "/hasMore" }
      ] },
      #{ source: "/columns", select: #[
        #{ source: "/name" },
        #{ source: "/type", target: "dataType" },
        #{ source: "/nullable" },
        #{ source: "/fieldRef", select: #[
          #{ source: "/type" },
          #{ source: "/id" }
        ] },
        #{ source: "/label" },
        #{ source: "/kind" },
        #{ source: "/unit" },
        #{ source: "/format" }
      ] },
      #{ source: "/rows" }
    ],
    cursor: #{ source: "/page/nextCursor" }
  },
})
op querySemanticModel(@path("model") modelName: ResourceId, @header accept?: string, @body body?: SemanticQueryRequest): RowsetResponse<SemanticQueryResponse> | CommonErrors;

@tag("BI")
@summary("Explain semantic model query")
@route("/semantic-models/{model}/query/explain")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
@extension("x-leapview-object-scope", "semantic-model")
@apigen.cli(#{ command: #["semantic-models", "model-explain"], args: #[#{ source: "path", name: "model", displayName: "model-id" }], output: #{ mode: "raw" } })
op explainSemanticModelQuery(@path("model") modelName: ResourceId, @body body?: SemanticQueryRequest): OkJson<SemanticExplainResponse> | CommonErrors;

@tag("BI")
@summary("List semantic model datasets")
@route("/semantic-models/{model}/datasets")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "semantic-model")
@apigen.cli(#{ command: #["semantic-models", "datasets"], args: #[#{ source: "path", name: "model", displayName: "model-id" }], output: #{ mode: "raw" } })
op listSemanticDatasets(@path("model") modelName: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticDatasetListResponse> | CommonErrors;

@tag("BI")
@summary("Describe semantic model dataset")
@route("/semantic-models/{model}/datasets/{dataset}")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "semantic-model")
@apigen.cli(#{ command: #["semantic-models", "dataset"], args: #[#{ source: "path", name: "model", displayName: "model-id" }, #{ source: "path", name: "dataset", displayName: "dataset" }], output: #{ mode: "raw" } })
op getSemanticDataset(@path("model") modelName: ResourceId, @path dataset: string): OkJson<SemanticDatasetResponse> | CommonErrors;

@tag("BI")
@summary("List semantic model dataset fields")
@route("/semantic-models/{model}/datasets/{dataset}/fields")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "semantic-model")
@apigen.cli(#{ command: #["semantic-models", "fields"], args: #[#{ source: "path", name: "model", displayName: "model-id" }, #{ source: "path", name: "dataset", displayName: "dataset" }], output: #{ mode: "raw" } })
op listSemanticFields(@path("model") modelName: ResourceId, @path dataset: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticFieldListResponse> | CommonErrors;

@tag("BI")
@summary("Preview semantic model dataset rows")
@route("/semantic-models/{model}/datasets/{dataset}/preview")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
@extension("x-leapview-object-scope", "semantic-model")
@extension("x-leapview-response-trailers", #["X-Next-Cursor"])
@apigen.cli(#{ command: #["semantic-models", "preview"], args: #[#{ source: "path", name: "model", displayName: "model-id" }, #{ source: "path", name: "dataset", displayName: "dataset" }], output: #{ mode: "raw" } })
op previewSemanticDataset(@path("model") modelName: ResourceId, @path dataset: string, @header accept?: string, @body body?: SemanticPreviewRequest): RowsetResponse<SemanticQueryResponse> | CommonErrors;

@tag("BI")
@summary("Explain semantic model dataset row preview")
@route("/semantic-models/{model}/datasets/{dataset}/preview/explain")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
@extension("x-leapview-object-scope", "semantic-model")
@apigen.cli(#{ command: #["semantic-models", "explain-preview"], args: #[#{ source: "path", name: "model", displayName: "model-id" }, #{ source: "path", name: "dataset", displayName: "dataset" }], output: #{ mode: "raw" } })
op explainSemanticPreview(@path("model") modelName: ResourceId, @path dataset: string, @body body?: SemanticPreviewRequest): OkJson<SemanticExplainResponse> | CommonErrors;

@tag("BI")
@summary("Query dashboard page")
@route("/dashboards/{dashboard}/pages/{page}/query")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
@extension("x-leapview-object-scope", "dashboard")
@apigen.cli(#{ command: #["dashboards", "query-page"], args: #[#{ source: "path", name: "dashboard", displayName: "dashboard-id" }, #{ source: "path", name: "page", displayName: "page" }], output: #{ mode: "raw" } })
op queryDashboardPage(@path dashboard: ResourceId, @path page: string, @body body?: DashboardPageQueryRequest): OkJson<DashboardPageQueryResponse> | CommonErrors;

@tag("BI")
@summary("Query an existing dashboard visual with governed filters and compact analytical output")
@route("/dashboards/{dashboard}/pages/{page}/visuals/{visual}/query")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
@extension("x-leapview-object-scope", "dashboard")
@extension("x-leapview-response-trailers", #["X-Next-Cursor"])
@apigen.cli(#{ command: #["dashboards", "visual-data"], args: #[#{ source: "path", name: "dashboard", displayName: "dashboard-id" }, #{ source: "path", name: "page", displayName: "page" }, #{ source: "path", name: "visual", displayName: "visual" }], output: #{ mode: "raw" } })
@apigen.tool(#{
  name: "query_dashboard_visual", effect: "read", tags: #["dashboard", "visual", "query"],
  input: #{ fields: #[
    #{ source: "path", name: "dashboard", mode: "model", description: "Stable dashboard resource ID to query." },
    #{ source: "body", name: "limit", default: 50 },
    #{ source: "body", name: "filterState", mode: "omit" },
    #{ source: "body", name: "interactionSelections", mode: "omit" },
    #{ source: "body", name: "spatialSelections", mode: "omit" },
  ] },
  output: #{ mode: "raw" },
})
op queryDashboardVisualData(@path dashboard: ResourceId, @path page: string, @path visual: string, @header accept?: string, @body body?: DashboardVisualQueryRequest): RowsetResponse<LeapViewVisualization.VisualizationEnvelope> | CommonErrors;

@tag("BI")
@summary("List dashboard filter values")
@route("/dashboards/{dashboard}/pages/{page}/filters/{filter}/values")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_USE" })
@extension("x-leapview-object-scope", "dashboard")
@apigen.cli(#{ command: #["dashboards", "filter-options"], args: #[#{ source: "path", name: "dashboard", displayName: "dashboard-id" }, #{ source: "path", name: "page", displayName: "page" }, #{ source: "path", name: "filter", displayName: "filter" }], output: #{ mode: "raw" } })
op listDashboardFilterValues(@path dashboard: ResourceId, @path page: string, @path filter: string, @query limit?: ListLimit, @query pageToken?: PageToken, @body body?: DashboardPageQueryRequest): OkJson<DashboardFilterOptionListResponse> | CommonErrors;

@tag("BI")
@summary("Update a dashboard's catalog appearance")
@route("/dashboards/{dashboard}/appearance")
@put
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
@extension("x-leapview-object-scope", "dashboard")
@apigen.command(#{ auditAction: "dashboard.appearance.updated", guarantee: "best-effort", failures: #[
  #{ kind: "invalid", statusCode: 422, code: "INVALID_DASHBOARD_APPEARANCE", publicDetail: "The selected dashboard appearance is invalid." },
] })
@apigen.auditPayload(DashboardAppearanceUpdatedAuditPayload)
@apigen.ui("dashboard.appearance.update")
op updateDashboardAppearance(
  @path @apigen.target dashboard: ResourceId,
  @body body: DashboardAppearancePatchRequest,
): OkJson<DashboardAppearanceResponse> | CommonErrors;
