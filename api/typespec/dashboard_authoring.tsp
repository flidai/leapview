using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Dashboard;

model DashboardAuthoringRevisionToken {
  revisionId: string;
  number: uint64;
  contentHash: string;
}

model DashboardAuthoringRevisionEvidence {
  id: string;
  number: uint64;
  contentHash: string;
  createdAt?: utcDateTime;
}

model DashboardAuthoringPublicationEvidence {
  revision: DashboardAuthoringRevisionEvidence;
  definitionHash?: string;
  semanticServingStateId?: string;
  publishedAt?: utcDateTime;
}

model DashboardAuthoringSourceRef {
  kind: "project";
  projectId: ResourceId;
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
  baseSemanticServingStateId?: string;
  source?: JSONObject;
  forkedFrom?: JSONObject;
}

model DashboardAuthoringEmptyPayload {}

model DashboardAuthoringSetVisibilityIntent {
  visibility: "private" | "shared";
}

model DashboardAuthoringAddPageIntent {
  pageId?: string;
  title?: string;
}

model DashboardAuthoringAddVisualIntent {
  pageId: string;
  visualId?: string;
  componentId?: string;
  type: string;
  title?: string;
}

model DashboardAuthoringAssignFieldIntent {
  pageId: string;
  visualId: string;
  fieldId: string;
  role: "measure" | "dimension" | "detail";
}

model DashboardAuthoringCommandRequest {
  dashboardId: ResourceId;
  draftId: string;
  expectedRevision: DashboardAuthoringRevisionToken;
  contentHash?: string;
  origin?: "ui" | "file" | "agent";
  setVisibility?: DashboardAuthoringSetVisibilityIntent;
  addPage?: DashboardAuthoringAddPageIntent;
  addVisual?: DashboardAuthoringAddVisualIntent;
  assignField?: DashboardAuthoringAssignFieldIntent;
  publish?: DashboardAuthoringEmptyPayload;
  archive?: DashboardAuthoringEmptyPayload;
}

model DashboardAuthoringSummary {
  id: ResourceId;
  stableId: ResourceId;
  title: string;
  description?: string;
  semanticModel: ResourceId;
  source: "project";
  origin: "ui" | "file" | "agent";
  status: "draft" | "published" | "archived";
  visibility: "private" | "shared";
  owner?: string;
  tags?: string[];
  servingStateId?: string;
  revision?: DashboardAuthoringRevisionEvidence;
  publication?: DashboardAuthoringPublicationEvidence;
  sourcePath?: string;
}

model DashboardAuthoringCatalogResponse {
  items: DashboardAuthoringSummary[];
  count: int32;
}

model DashboardAuthoringMutationResponse {
  revision: DashboardAuthoringRevisionToken;
  lifecycle: JSONObject;
}

model DashboardAuthoringDraftResponse {
  dashboardId: ResourceId;
  draftId: string;
  revision: DashboardAuthoringRevisionToken;
  lifecycle: JSONObject;
  document: JSONObject;
}

model DashboardAuthoringRevisionResponse {
  dashboardId: ResourceId;
  revision: DashboardAuthoringRevisionToken;
  document: JSONObject;
  provenance: DashboardAuthoringProvenance;
  createdAt: utcDateTime;
}

model DashboardAuthoringPreviewRequest {
  revision: DashboardAuthoringRevisionToken;
  pageId: string;
  filters?: JSONObject;
}

model DashboardAuthoringPreviewEvidence {
  semanticModel: string;
  runtimeModel: string;
  servingStateId: string;
  duckLakeSnapshotId: int64;
}

model DashboardAuthoringPreviewResponse {
  revision: DashboardAuthoringRevisionToken;
  definition: JSONObject;
  pagePatch: JSONObject;
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
  dashboardId: string;
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
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
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
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @apigen.command(#{ auditAction: "dashboard_authoring.draft_created", guarantee: "best-effort", failures: #[] })
  createDashboardAuthoringDraft(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DashboardAuthoringCreateRequest): CreatedJson<DashboardAuthoringMutationResponse> | CommonErrors;

  @summary("Execute one dashboard authoring command")
  @route("/commands")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
  @apigen.ui("dashboard.builder.command")
  @apigen.command(#{ auditAction: "dashboard_authoring.command_executed", guarantee: "best-effort", failures: #[] })
  executeDashboardAuthoringCommand(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DashboardAuthoringCommandRequest): OkJson<DashboardAuthoringMutationResponse> | CommonErrors;

  @summary("Fork a dashboard source into a new draft")
  @route("/forks")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_EDIT" })
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
