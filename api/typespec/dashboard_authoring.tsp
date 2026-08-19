import "../dashboard/main.tsp";

using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Dashboard;

model DashboardAuthoringRevisionToken {
  revisionId: string;
  @minValue(0)
  number: int64;
  contentHash: string;
}

model DashboardAuthoringRevisionEvidence {
  id: string;
  @minValue(0)
  number: int64;
  contentHash: string;
  createdAt?: utcDateTime;
}

/** Exact graph serving identity persisted with authored provenance and compilation evidence. */
model DashboardAuthoringServingIdentity {
  projectId: ResourceId;
  environment: string;
  generationId: string;
}

model DashboardAuthoringCompiledRevisionToken {
  authoredRevision: DashboardAuthoringRevisionToken;
  definitionHash: string;
  semanticModelId: ResourceId;
  semanticIdentity: DashboardAuthoringServingIdentity;
}

model DashboardAuthoringPublicationEvidence {
  revision: DashboardAuthoringRevisionEvidence;
  definitionHash?: string;
  semanticIdentity: DashboardAuthoringServingIdentity;
  publishedAt?: utcDateTime;
}

model DashboardAuthoringSourceRef {
  // The route's project identifies project-managed sources; instance sources
  // use the same dashboard identity without an optional project selector.
  kind: "project" | "instance";
  dashboardId: ResourceId;
}

model DashboardAuthoringCreateRequest {
  title: string;
  semanticModel: ResourceId;
  slug?: string;
  origin?: "ui" | "file" | "agent";
}

model DashboardAuthoringForkRequest {
  source: DashboardAuthoringSourceRef;
  title?: string;
  slug?: string;
  origin?: "ui" | "file" | "agent";
}

// Provenance is response evidence; mutation requests accept only the bounded
// origin enum and bind actor/command identity from authenticated transport.
model DashboardAuthoringProvenance {
  origin: "ui" | "file" | "agent";
  actorId: string;
  conversationId?: string;
  toolCallId?: string;
  baseSemanticIdentity?: DashboardAuthoringServingIdentity;
  source?: DashboardAuthoringSourceMetadata;
  forkedFrom?: DashboardAuthoringForkEvidence;
}

model DashboardAuthoringSourceMetadata {
  path?: string;
  repository?: string;
  ref?: string;
  revision?: string;
  metadata?: Record<string>;
}

@discriminator("kind")
model DashboardAuthoringForkEvidence {
  kind: "instance" | "project";
}

model DashboardAuthoringInstanceForkEvidence extends DashboardAuthoringForkEvidence {
  kind: "instance";
  instance: DashboardAuthoringInstanceForkEvidenceData;
}

model DashboardAuthoringInstanceForkEvidenceData {
  sourceProjectId: ResourceId;
  sourceDashboardId: ResourceId;
  sourceRevision: DashboardAuthoringRevisionToken;
}

model DashboardAuthoringProjectForkEvidence extends DashboardAuthoringForkEvidence {
  kind: "project";
  project: DashboardAuthoringProjectForkEvidenceData;
}

model DashboardAuthoringProjectForkEvidenceData {
  sourceProjectId: ResourceId;
  sourceDashboardId: ResourceId;
  identity: DashboardAuthoringServingIdentity;
  path?: string;
}

model DashboardAuthoringDraftPointer {
  id: string;
  dashboardId: ResourceId;
  revision: DashboardAuthoringRevisionToken;
  provenance: DashboardAuthoringProvenance;
}

model DashboardAuthoringPublishedPointer {
  revision: DashboardAuthoringRevisionToken;
  compilation: DashboardAuthoringCompiledRevisionToken;
  publishedAt?: utcDateTime;
  provenance: DashboardAuthoringProvenance;
}

model DashboardAuthoringRevalidationFailure {
  identity: DashboardAuthoringServingIdentity;
  dependencyIds?: ResourceId[];
  code: string;
  message: string;
  failedAt: utcDateTime;
}

model DashboardAuthoringLifecycle {
  projectId: ResourceId;
  id: ResourceId;
  ownerPrincipalId: string;
  slug: string;
  title: string;
  semanticModel: ResourceId;
  visibility: "private" | "restricted" | "organization";
  status: "draft" | "published" | "archived";
  draft?: DashboardAuthoringDraftPointer;
  published?: DashboardAuthoringPublishedPointer;
  revalidation?: DashboardAuthoringRevalidationFailure;
}

model DashboardAuthoringEmptyPayload {}

model DashboardAuthoringSetVisibilityIntent {
  visibility: "private" | "restricted" | "organization";
}

model DashboardAuthoringAddPageIntent {
  pageId?: string;
  title?: string;
}

model DashboardAuthoringAddVisualIntent {
  pageId: string;
  visualId?: string;
  componentId?: string;
  type: LeapViewDashboard.DashboardVisualType;
  title?: string;
}

model DashboardAuthoringMetadataIntent {
  title?: string;
  description?: string;
  slug?: string;
  semanticModel?: ResourceId;
  visibility?: "private" | "restricted" | "organization";
  appearance?: LeapViewDashboard.DashboardAppearance;
}

model DashboardAuthoringUpsertVisualIntent {
  visualId: string;
  visual: LeapViewDashboard.DashboardVisual;
}

model DashboardAuthoringRemovePageIntent { pageId: string; }
model DashboardAuthoringRemoveVisualIntent { visualId: string; }
model DashboardAuthoringSetLayoutIntent {
  pageId: string;
  layout?: LeapViewDashboard.DashboardLayoutOverride;
}
model DashboardAuthoringSetFiltersIntent {
  filters?: LeapViewDashboard.DashboardFilter[];
  clear?: boolean;
}
model DashboardAuthoringSetInteractionIntent {
  pageId?: string;
  visualId?: string;
  interaction?: LeapViewDashboard.DashboardInteraction;
  clear?: boolean;
}

model DashboardAuthoringAssignFieldIntent {
  pageId: string;
  visualId: string;
  fieldId: string;
  role: "metric" | "dimension" | "detail";
}

@discriminator("kind")
model DashboardAuthoringCommandRequest {
  kind: string;
  dashboardId: ResourceId;
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  contentHash?: string;
  origin?: "ui" | "file" | "agent";
}

model DashboardAuthoringMetadataCommand extends DashboardAuthoringCommandRequest {
  kind: "metadata";
  metadata: DashboardAuthoringMetadataIntent;
}

model DashboardAuthoringSetVisibilityCommand extends DashboardAuthoringCommandRequest {
  kind: "setVisibility";
  setVisibility: DashboardAuthoringSetVisibilityIntent;
}

model DashboardAuthoringAddPageCommand extends DashboardAuthoringCommandRequest {
  kind: "addPage";
  addPage: DashboardAuthoringAddPageIntent;
}

model DashboardAuthoringAddVisualCommand extends DashboardAuthoringCommandRequest {
  kind: "addVisual";
  addVisual: DashboardAuthoringAddVisualIntent;
}

model DashboardAuthoringAssignFieldCommand extends DashboardAuthoringCommandRequest {
  kind: "assignField";
  assignField: DashboardAuthoringAssignFieldIntent;
}

model DashboardAuthoringUpsertPageCommand extends DashboardAuthoringCommandRequest {
  kind: "upsertPage";
  upsertPage: LeapViewDashboard.DashboardPage;
}

model DashboardAuthoringRemovePageCommand extends DashboardAuthoringCommandRequest {
  kind: "removePage";
  removePage: DashboardAuthoringRemovePageIntent;
}

model DashboardAuthoringUpsertVisualCommand extends DashboardAuthoringCommandRequest {
  kind: "upsertVisual";
  upsertVisual: DashboardAuthoringUpsertVisualIntent;
}

model DashboardAuthoringRemoveVisualCommand extends DashboardAuthoringCommandRequest {
  kind: "removeVisual";
  removeVisual: DashboardAuthoringRemoveVisualIntent;
}

model DashboardAuthoringSetLayoutCommand extends DashboardAuthoringCommandRequest {
  kind: "setLayout";
  setLayout: DashboardAuthoringSetLayoutIntent;
}

model DashboardAuthoringSetFiltersCommand extends DashboardAuthoringCommandRequest {
  kind: "setFilters";
  setFilters: DashboardAuthoringSetFiltersIntent;
}

model DashboardAuthoringSetInteractionCommand extends DashboardAuthoringCommandRequest {
  kind: "setInteraction";
  setInteraction: DashboardAuthoringSetInteractionIntent;
}

model DashboardAuthoringPublishCommand extends DashboardAuthoringCommandRequest {
  kind: "publish";
  publish: DashboardAuthoringEmptyPayload;
}

model DashboardAuthoringArchiveCommand extends DashboardAuthoringCommandRequest {
  kind: "archive";
  archive: DashboardAuthoringEmptyPayload;
}

model DashboardAuthoringSummary {
  id: ResourceId;
  stableId: string;
  projectId: ResourceId;
  title: string;
  description?: string;
  semanticModel: ResourceId;
  source: "project" | "instance";
  origin: "ui" | "file" | "agent";
  status: "draft" | "published" | "archived";
  visibility: "private" | "restricted" | "organization";
  owner?: string;
  tags?: string[];
  servingIdentity?: DashboardAuthoringServingIdentity;
  revision?: DashboardAuthoringRevisionEvidence;
  publication?: DashboardAuthoringPublicationEvidence;
  sourcePath?: string;
}

model DashboardAuthoringCatalogResponse {
  items: DashboardAuthoringSummary[];
  count: int32;
  instanceCount: int32;
  projectCount: int32;
}

model DashboardAuthoringMutationResponse {
  revision: DashboardAuthoringRevisionToken;
  lifecycle: DashboardAuthoringLifecycle;
}

model DashboardAuthoringDraftResponse {
  projectId: ResourceId;
  dashboardId: ResourceId;
  draftId: string;
  revision: DashboardAuthoringRevisionToken;
  lifecycle: DashboardAuthoringLifecycle;
  document: LeapViewDashboard.DashboardDocument;
}

model DashboardAuthoringRevisionResponse {
  projectId: ResourceId;
  dashboardId: ResourceId;
  revision: DashboardAuthoringRevisionToken;
  document: LeapViewDashboard.DashboardDocument;
  provenance: DashboardAuthoringProvenance;
  createdAt: utcDateTime;
}

model DashboardAuthoringPreviewRequest {
  revision: DashboardAuthoringRevisionToken;
  pageId: string;
  filters?: DashboardAuthoringPreviewFilters;
}

model DashboardAuthoringPreviewFilters {
  selections: DashboardAuthoringPreviewSelection[];
  spatialSelections: DashboardAuthoringPreviewSpatialSelection[];
  @minValue(0)
  interactionRevision: int64;
}

model DashboardAuthoringPreviewSelection {
  id: string;
  sourceKind: "visual";
  sourceId: string;
  interactionKind: "point_selection" | "row_selection";
  entries: DashboardAuthoringPreviewSelectionEntry[];
  label: string;
  order: int32;
}

model DashboardAuthoringPreviewSelectionEntry {
  mappings: DashboardAuthoringPreviewSelectionMapping[];
  label?: string;
}

model DashboardAuthoringPreviewSelectionMapping {
  field: string;
  dataset?: string;
  grain?: LeapViewDashboard.DashboardTimeGrain;
  value: DashboardAuthoringPreviewJSONScalar;
  label?: string;
}

union DashboardAuthoringPreviewJSONScalar {
  text: string,
  number: float64,
  boolean: boolean,
  nil: null,
}

model DashboardAuthoringPreviewSpatialSelection {
  visualID: string;
  interactionID: string;
  gesture: LeapViewVisualization.VisualizationSpatialSelectionGesture;
  geometry: LeapViewVisualization.VisualizationSpatialSelectionGeometry;
  order: int32;
}

model DashboardAuthoringPreviewStatus {
  loading: boolean;
  error: string;
  refreshId: string;
  generation: int64;
  lastUpdated: string;
  setupRequired: boolean;
  progressPercent?: float64;
}

model DashboardAuthoringPreviewPagePatch {
  filters: DashboardAuthoringPreviewFilters;
  status: DashboardAuthoringPreviewStatus;
  visuals: Record<LeapViewVisualization.VisualizationEnvelope>;
}

model DashboardAuthoringPreviewPage {
  id: string;
  title: string;
  description?: string;
  canvas: DashboardAuthoringPreviewCanvas;
  grid: DashboardAuthoringPreviewGrid;
  responsiveLayout?: Record<unknown>;
  filterBindings?: Record<unknown>;
  visuals: DashboardAuthoringPreviewPageVisual[];
  width?: int32;
  height?: int32;
}

model DashboardAuthoringPreviewCanvas {
  width: int32;
  height: int32;
}

model DashboardAuthoringPreviewGrid {
  columns: int32;
  rowHeight: int32;
  gap: int32;
  padding: int32;
}

model DashboardAuthoringPreviewPageVisual {
  id: string;
  kind: string;
  visual?: string;
  binding?: Record<unknown>;
  presentation?: Record<unknown>;
  description?: string;
  eyebrow?: string;
  title?: string;
  subtitle?: string;
  badges?: string[];
  placement: DashboardAuthoringPreviewPlacement;
  x: float64;
  y: float64;
  width: float64;
  height: float64;
}

model DashboardAuthoringPreviewPlacement {
  col: int32;
  row: int32;
  colSpan: int32;
  rowSpan: int32;
}

model DashboardAuthoringPreviewVisualization {
  id: string;
  rendererID: string;
  specRevision: string;
  spec: LeapViewVisualization.VisualizationSpec;
  query: DashboardAuthoringPreviewQueryBinding;
  secondaryQueries?: Record<DashboardAuthoringPreviewQueryBinding>;
}

@discriminator("kind")
model DashboardAuthoringPreviewQueryBinding {
  kind: string;
  resultShape: string;
  modelID: string;
  datasetID: string;
  identity?: string[];
}

model DashboardAuthoringPreviewAggregateQuery extends DashboardAuthoringPreviewQueryBinding {
  kind: "aggregate";
  aggregate: DashboardAuthoringPreviewAggregateQueryBinding;
}

model DashboardAuthoringPreviewDetailQuery extends DashboardAuthoringPreviewQueryBinding {
  kind: "detail";
  detail: DashboardAuthoringPreviewDetailQueryBinding;
}

model DashboardAuthoringPreviewMatrixQuery extends DashboardAuthoringPreviewQueryBinding {
  kind: "matrix";
  matrix: DashboardAuthoringPreviewMatrixQueryBinding;
}

model DashboardAuthoringPreviewPivotQuery extends DashboardAuthoringPreviewQueryBinding {
  kind: "pivot";
  pivot: DashboardAuthoringPreviewPivotQueryBinding;
}

model DashboardAuthoringPreviewSpatialQuery extends DashboardAuthoringPreviewQueryBinding {
  kind: "spatial";
  spatial: DashboardAuthoringPreviewSpatialQueryBinding;
}

model DashboardAuthoringPreviewFieldBinding {
  `fieldID`: string;
  `alias`: string;
  grain?: LeapViewDashboard.DashboardTimeGrain;
}

model DashboardAuthoringPreviewTimeBinding {
  `fieldID`: string;
  `alias`: string;
  grain: LeapViewDashboard.DashboardTimeGrain;
}

model DashboardAuthoringPreviewAggregateQueryBinding {
  tableID: string;
  dimensions: DashboardAuthoringPreviewFieldBinding[];
  series?: DashboardAuthoringPreviewFieldBinding;
  metrics: DashboardAuthoringPreviewFieldBinding[];
  time?: DashboardAuthoringPreviewTimeBinding;
  sort?: DashboardAuthoringPreviewSort[];
  limit: int64;
  histogram?: Record<unknown>;
  distribution?: Record<unknown>;
}

model DashboardAuthoringPreviewDetailQueryBinding {
  tableID: string;
  fields: DashboardAuthoringPreviewFieldBinding[];
  defaultSort?: DashboardAuthoringPreviewSort[];
  limit: int64;
}

model DashboardAuthoringPreviewMatrixQueryBinding {
  tableID: string;
  rows: DashboardAuthoringPreviewFieldBinding[];
  columns: DashboardAuthoringPreviewFieldBinding[];
  metrics: DashboardAuthoringPreviewFieldBinding[];
  limit: int64;
}

model DashboardAuthoringPreviewPivotQueryBinding {
  tableID: string;
  rows: DashboardAuthoringPreviewFieldBinding[];
  columns: DashboardAuthoringPreviewFieldBinding[];
  metrics: DashboardAuthoringPreviewFieldBinding[];
  sort?: DashboardAuthoringPreviewSort[];
  offset?: int64;
  totals?: DashboardAuthoringPreviewPivotTotals;
  limit: int64;
}

model DashboardAuthoringPreviewSpatialQueryBinding {
  tableID: string;
  dimensions?: DashboardAuthoringPreviewFieldBinding[];
  series?: DashboardAuthoringPreviewFieldBinding;
  metrics?: DashboardAuthoringPreviewFieldBinding[];
  time?: DashboardAuthoringPreviewTimeBinding;
  sort?: DashboardAuthoringPreviewSort[];
  limit: int64;
  tiles?: Record<unknown>;
}

model DashboardAuthoringPreviewSort {
  field: string;
  direction: "asc" | "desc";
}

model DashboardAuthoringPreviewPivotTotals {
  rows: boolean;
  columns: boolean;
  grand: boolean;
}

model DashboardAuthoringPreviewDefinition {
  id: string;
  title: string;
  description?: string;
  semanticModel: string;
  layout?: Record<unknown>;
  filterDefinitions?: Record<unknown>;
  filterBindings?: Record<unknown>;
  filterApplication?: Record<unknown>;
  filterOrder?: string[];
  pages: DashboardAuthoringPreviewPage[];
  visualizations: Record<DashboardAuthoringPreviewVisualization>;
}

model DashboardAuthoringPreviewEvidence {
  semanticModel: string;
  runtimeModel: string;
  identity: DashboardAuthoringServingIdentity;
  duckLakeSnapshotId: int64;
}

model DashboardAuthoringPreviewResponse {
  revision: DashboardAuthoringRevisionToken;
  definition: DashboardAuthoringPreviewDefinition;
  pagePatch: DashboardAuthoringPreviewPagePatch;
  semanticEvidence: DashboardAuthoringPreviewEvidence;
}

model DashboardAuthoringYAMLResponse {
  ...OkResponse;
  @header contentType: "application/yaml";
  @header contentDisposition: string;
  @body body: bytes;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DashboardAuthoringCommandAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  dashboardId: ResourceId;
  @apigen.auditInternal
  draftId: string;
  @apigen.auditPublic
  origin: "ui" | "file" | "agent";
}

@tag("Dashboard Authoring")
@doc("Headless project-wide dashboard authoring identities, draft revisions, commands, forks, previews, and source export.")
@route("/projects/{project}/authoring")
@apigen.authz(#{ mode: "authenticated" })
@apigen.auditPayload(DashboardAuthoringCommandAuditPayload)
interface DashboardAuthoring {
  @summary("List governed dashboard authoring identities")
  @route("/catalog")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listDashboardAuthoringCatalog(@path project: ResourceId): OkJson<DashboardAuthoringCatalogResponse> | CommonErrors;

  @summary("Get one governed dashboard authoring identity")
  @route("/dashboards/{dashboard}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "dashboard")
  getDashboardAuthoringDashboard(@path project: ResourceId, @path dashboard: ResourceId): OkJson<DashboardAuthoringSummary> | CommonErrors;

  @summary("Get the exact current dashboard draft")
  @route("/dashboards/{dashboard}/draft")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @extension("x-leapview-object-scope", "dashboard")
  getDashboardAuthoringDraft(@path project: ResourceId, @path dashboard: ResourceId): OkJson<DashboardAuthoringDraftResponse> | CommonErrors;

  @summary("Get an exact draft revision")
  @route("/dashboards/{dashboard}/drafts/{draft}/revisions/{revision}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @extension("x-leapview-object-scope", "dashboard")
  getDashboardAuthoringDraftRevision(@path project: ResourceId, @path dashboard: ResourceId, @path draft: string, @path revision: string): OkJson<DashboardAuthoringRevisionResponse> | CommonErrors;

  @summary("Get the exact published dashboard revision")
  @route("/dashboards/{dashboard}/revisions/{revision}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "dashboard")
  getDashboardAuthoringPublishedRevision(@path project: ResourceId, @path dashboard: ResourceId, @path revision: string): OkJson<DashboardAuthoringRevisionResponse> | CommonErrors;

  @summary("Create a named dashboard draft")
  @route("/drafts")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.command(#{ auditAction: "dashboard_authoring.draft_created", guarantee: "best-effort", failures: #[] })
  createDashboardAuthoringDraft(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DashboardAuthoringCreateRequest): CreatedJson<DashboardAuthoringMutationResponse> | CommonErrors;

  @summary("Execute one dashboard authoring command")
  @route("/commands")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("dashboard.builder.command")
  @apigen.command(#{ auditAction: "dashboard_authoring.command_executed", guarantee: "best-effort", failures: #[] })
  executeDashboardAuthoringCommand(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DashboardAuthoringCommandRequest): OkJson<DashboardAuthoringMutationResponse> | CommonErrors;

  @summary("Fork a dashboard source into a new draft")
  @route("/forks")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.command(#{ auditAction: "dashboard_authoring.draft_forked", guarantee: "best-effort", failures: #[] })
  forkDashboardAuthoringDraft(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DashboardAuthoringForkRequest): CreatedJson<DashboardAuthoringMutationResponse> | CommonErrors;

  @summary("Preview one exact dashboard draft revision")
  @route("/dashboards/{dashboard}/drafts/{draft}/preview")
  @post
  @apigen.query
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @extension("x-leapview-object-scope", "dashboard")
  previewDashboardAuthoringDraft(@path project: ResourceId, @path dashboard: ResourceId, @path draft: string, @body body: DashboardAuthoringPreviewRequest): OkJson<DashboardAuthoringPreviewResponse> | CommonErrors;

  @summary("Export one governed dashboard source as YAML")
  @route("/sources/{kind}/{dashboard}/export")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "dashboard")
  exportDashboardAuthoringSource(@path project: ResourceId, @path kind: string, @path dashboard: ResourceId): DashboardAuthoringYAMLResponse | CommonErrors;
}
