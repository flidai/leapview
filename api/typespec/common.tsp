using Http;
using TypeSpec.OpenAPI;

namespace LeapViewAPI.Protocol;

@minLength(1)
@maxLength(200)
@pattern("^[A-Za-z0-9][A-Za-z0-9_.:-]*$")
scalar ResourceId extends string;

enum ResourceKind {
  project;
  connection;
  sourceKind: "source";
  modelKind: "model";
  semanticModel: "semantic_model";
  pipelineKind: "pipeline";
  dashboardKind: "dashboard";
}

@minLength(1)
@maxLength(64)
@pattern("^[A-Za-z][A-Za-z0-9_.:-]*$")
scalar AuditTargetKind extends string;

enum Capability {
  platformAdmin: "PLATFORM_ADMIN";
  projectAdmin: "PROJECT_ADMIN";
  resourceUse: "RESOURCE_USE";
  resourceRead: "RESOURCE_READ";
  resourceEdit: "RESOURCE_EDIT";
  resourceManage: "RESOURCE_MANAGE";
  resourceShare: "RESOURCE_SHARE";
  resourcePublish: "RESOURCE_PUBLISH";
}

/**
 * Typed action identities for the versioned resource-authorization catalog.
 * Current-user token creation uses these paired action/target values. The
 * legacy Capability enum remains only for compatibility and bootstrap flows.
 */
enum PermissionAction {
  dashboardRead: "dashboard.read";
  dashboardCreate: "dashboard.create";
  dashboardUpdate: "dashboard.update";
  dashboardDelete: "dashboard.delete";
  dashboardPublish: "dashboard.publish";

  semanticRead: "semantic.read";
  semanticQuery: "semantic.query";
  semanticConsume: "semantic.consume";
  semanticCreate: "semantic.create";
  semanticUpdate: "semantic.update";
  semanticDelete: "semantic.delete";

  sourceRead: "source.read";
  sourceCreate: "source.create";
  sourceUpdate: "source.update";
  sourceDelete: "source.delete";

  modelRead: "model.read";
  modelCreate: "model.create";
  modelUpdate: "model.update";
  modelDelete: "model.delete";

  pipelineRead: "pipeline.read";
  pipelineCreate: "pipeline.create";
  pipelineRun: "pipeline.run";
  pipelineUpdate: "pipeline.update";
  pipelineDelete: "pipeline.delete";

  connectionRead: "connection.read";
  connectionCreate: "connection.create";
  connectionUse: "connection.use";
  connectionManage: "connection.manage";

  resourceShare: "resource.share";

  deliveryRead: "delivery.read";
  deliveryPlan: "delivery.plan";
  deliveryBuild: "delivery.build";
  deliveryPublish: "delivery.publish";
  deliveryApprove: "delivery.approve";
  deliveryActivate: "delivery.activate";
  deliveryRollback: "delivery.rollback";

  projectSettingsRead: "project.settings.read";
  projectSettingsUpdate: "project.settings.update";
  projectAccessRead: "project.access.read";
  projectAccessManage: "project.access.manage";
  projectAccessDelegate: "project.access.delegate";
  auditRead: "audit.read";

  workloadDelegate: "workload.delegate";

  platformSettingsRead: "platform.settings.read";
  platformSettingsUpdate: "platform.settings.update";
  platformAccessRead: "platform.access.read";
  platformAccessManage: "platform.access.manage";
  platformAuditRead: "platform.audit.read";
}

/** The catalog profile pinned by a typed permission pair. */
enum PermissionCatalogProfile {
  v1: "leapview.permissions/v1";
}

enum PermissionScope {
  instance;
  project;
  resource;
}

/**
 * The audience and resource half of one typed permission pair. The optional
 * fields are structurally constrained by scope and action at runtime.
 */
@example(#{
  scope: PermissionScope.resource,
  projectId: "project:analytics",
  resourceKind: ResourceKind.dashboardKind,
  resourceId: "dashboard:sales",
})
model PermissionTarget {
  scope: PermissionScope;
  instanceId?: string;
  projectId?: ResourceId;
  resourceKind?: ResourceKind;
  resourceId?: ResourceId;
  includeFuture?: boolean;
}

/** Preserve the action/target relationship through authorization boundaries. */
model PermissionPair {
  action: PermissionAction;
  target: PermissionTarget;
  profile: PermissionCatalogProfile;
}

@minValue(1)
@maxValue(200)
scalar ListLimit extends int32;

@minValue(1)
@maxValue(1000)
scalar QueryLimit extends int32;

@minLength(1)
@maxLength(2048)
scalar PageToken extends string;

@minLength(1)
@maxLength(200)
scalar IdempotencyKey extends string;

@minLength(36)
@maxLength(36)
@pattern("^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
scalar UUIDv7IdempotencyKey extends string;

model EmptyResponse {
  @statusCode statusCode: 204;
}


model ProblemFieldError {
  field: string;
  code: string;
  detail: string;
}

model ProblemDetails {
  type: string;
  title: string;
  status: int32;
  detail: string;
  instance: string;
  code: string;
  requestId: string;
  errors: ProblemFieldError[];
}

model PageInfo {
  nextCursor?: string;
}

model StatusResponse {
  status: string;
}

model AsyncProgress {
  current?: int64;
  total?: int64;
  percent?: float64;
  message?: string;
}

model AsyncStructuredError {
  code: string;
  detail: string;
}

model AsyncEventResponse {
  id: string;
  event: string;
  resourceType: string;
  resourceId: string;
  createdAt: utcDateTime;
  progress?: AsyncProgress;
  error?: AsyncStructuredError;
  data: Record<unknown>;
}

model AsyncEventListResponse {
  items: AsyncEventResponse[];
  page: PageInfo;
}

alias JSONObject = Record<unknown>;

model OkJson<T> {
  ...OkResponse;
  ...Body<T>;
}

model CreatedJson<T> {
  ...CreatedResponse;
  @header location: string;
  ...Body<T>;
}

model AcceptedJson<T> {
  ...Response<202>;
  @header location: string;
  ...Body<T>;
}

@extension("x-leapview-response-trailers", #["X-Next-Cursor"])
model ArrowStreamResponse {
  ...OkResponse;
  @header contentType: "application/vnd.apache.arrow.stream";
  @header("X-Query-ID") queryId: string;
  @header("X-Serving-Snapshot") servingSnapshot: string;
  @header("X-LeapView-Arrow-Contract") arrowContract: "native-v1";
  @header("Trailer") trailers: "X-Next-Cursor";
  @header cacheControl: "no-store";
  @body body: bytes;
}

model EventStreamResponse {
  ...OkResponse;
  @header contentType: "text/event-stream";
  @header cacheControl: "no-store";
  @body body: string;
}

alias RowsetResponse<T> = OkJson<T> | ArrowStreamResponse;
alias EventHistoryResponse<T> = OkJson<T> | EventStreamResponse;

model ProblemResponse<Status> {
  ...Response<Status>;
  @header contentType: "application/problem+json";
  @body body: ProblemDetails;
}

model BadRequest {
  ...BadRequestResponse;
  @header contentType: "application/problem+json";
  @body body: ProblemDetails;
}

model Unauthorized {
  ...UnauthorizedResponse;
  @header contentType: "application/problem+json";
  @body body: ProblemDetails;
}

model Forbidden {
  ...ForbiddenResponse;
  @header contentType: "application/problem+json";
  @body body: ProblemDetails;
}

model NotFound {
  ...NotFoundResponse;
  @header contentType: "application/problem+json";
  @body body: ProblemDetails;
}

model Conflict {
  ...ConflictResponse;
  @header contentType: "application/problem+json";
  @body body: ProblemDetails;
}

model RateLimited {
  ...ProblemResponse<429>;
}

model InternalServerError {
  ...ProblemResponse<500>;
}

model PreconditionFailed {
  ...ProblemResponse<412>;
}

model ContentTooLarge {
  ...ProblemResponse<413>;
}

model UnsupportedMediaType {
  ...ProblemResponse<415>;
}

model UnprocessableContent {
  ...ProblemResponse<422>;
}

model BadGateway {
  ...ProblemResponse<502>;
}

model ServiceUnavailable {
  ...ProblemResponse<503>;
}

model GatewayTimeout {
  ...ProblemResponse<504>;
}

alias CommonErrors = BadRequest | Unauthorized | Forbidden | NotFound | Conflict | PreconditionFailed | ContentTooLarge | UnsupportedMediaType | UnprocessableContent | RateLimited | InternalServerError | BadGateway | ServiceUnavailable;
alias RowsetErrors = CommonErrors | GatewayTimeout;
