using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Dashboard;

enum DashboardPublicationStatus {
  active;
  suspended;
  unconfigured;
}

model DashboardPublicationResponse {
  name: string;
  projectId: ResourceId;
  dashboard: ResourceId;
  defaultPage: string;
  status: DashboardPublicationStatus;
  configured: boolean;
  allowedOrigins: string[];
  activeServingStateId?: string;
  publicUrl: string;
  embedUrl: string;
  iframeSnippet: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  configuredAt?: utcDateTime;
  disabledAt?: utcDateTime;
  suspendedAt?: utcDateTime;
  suspendedBy?: string;
  rotatedAt?: utcDateTime;
}

model DashboardPublicationListResponse {
  items: DashboardPublicationResponse[];
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DashboardPublicationCommandAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  owner: string;
  @apigen.auditPublic
  surface: string;
}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "PUBLICATION_NOT_FOUND", publicDetail: "Dashboard publication not found." })
model DashboardPublicationNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "PUBLICATION_NOT_CONFIGURED", publicDetail: "Dashboard publication is not present in the active configuration." })
model DashboardPublicationNotConfiguredFailure {}

@tag("Publications")
@doc("Project-wide dashboard publication discovery and lifecycle commands. Publication mutations complete synchronously and return the updated publication; they do not create durable asynchronous executions.")
@route("/projects/{project}/dashboard-publications")
@apigen.authz(#{ mode: "authenticated" })
@apigen.commandDefaults(#{ guarantee: "best-effort" })
@apigen.auditPayload(DashboardPublicationCommandAuditPayload)
interface DashboardPublications {
  @summary("List project dashboard publications")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listDashboardPublications(@path project: ResourceId): OkJson<DashboardPublicationListResponse> | CommonErrors;

  @summary("Get a dashboard publication")
  @route("/{publication}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  getDashboardPublication(@path project: ResourceId, @path publication: string): OkJson<DashboardPublicationResponse> | CommonErrors;

  @summary("Suspend a dashboard publication immediately")
  @route("/{publication}/suspend")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("dashboard.publication.suspend")
  @apigen.failsWith(DashboardPublicationNotFoundFailure)
  @apigen.failsWith(DashboardPublicationNotConfiguredFailure)
  @apigen.command(#{
    auditAction: "dashboard_publication.suspended",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Dashboard publication audit could not be persisted." }],
  })
  suspendDashboardPublication(@path @apigen.target project: ResourceId, @path publication: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DashboardPublicationResponse> | CommonErrors;

  @summary("Resume a configured dashboard publication")
  @route("/{publication}/resume")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("dashboard.publication.resume")
  @apigen.failsWith(DashboardPublicationNotFoundFailure)
  @apigen.failsWith(DashboardPublicationNotConfiguredFailure)
  @apigen.command(#{
    auditAction: "dashboard_publication.resumed",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Dashboard publication audit could not be persisted." }],
  })
  resumeDashboardPublication(@path @apigen.target project: ResourceId, @path publication: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DashboardPublicationResponse> | CommonErrors;

  @summary("Rotate a dashboard publication public ID")
  @route("/{publication}/rotate")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("dashboard.publication.rotate")
  @apigen.failsWith(DashboardPublicationNotFoundFailure)
  @apigen.failsWith(DashboardPublicationNotConfiguredFailure)
  @apigen.command(#{
    auditAction: "dashboard_publication.public_id_rotated",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Dashboard publication audit could not be persisted." }],
  })
  rotateDashboardPublication(@path @apigen.target project: ResourceId, @path publication: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DashboardPublicationResponse> | CommonErrors;
}
