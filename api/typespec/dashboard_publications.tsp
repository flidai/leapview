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
  workspaceId: string;
  projectId: string;
  dashboard: string;
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

@tag("Publications")
@doc("Dashboard publication discovery and lifecycle commands within a workspace.")
@route("/workspaces/{workspace}/dashboard-publications")
interface DashboardPublications {
  @summary("List dashboard publications in a workspace")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PUBLICATIONS" })
  @operationId("listDashboardPublications")
  listDashboardPublications(@path workspace: string): OkJson<DashboardPublicationListResponse> | CommonErrors;

  @summary("Get a dashboard publication")
  @route("/{publication}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PUBLICATIONS" })
  @operationId("getDashboardPublication")
  getDashboardPublication(@path workspace: string, @path publication: string): OkJson<DashboardPublicationResponse> | CommonErrors;

  @summary("Suspend a dashboard publication immediately")
  @route("/{publication}/suspend")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PUBLICATIONS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "dashboard_publication.suspended", guarantee: "best-effort" },
    targetParameter: "workspace",
  })
  @operationId("suspendDashboardPublication")
  suspendDashboardPublication(@path workspace: string, @path publication: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DashboardPublicationResponse> | CommonErrors;

  @summary("Resume a configured dashboard publication")
  @route("/{publication}/resume")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PUBLICATIONS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "dashboard_publication.resumed", guarantee: "best-effort" },
    targetParameter: "workspace",
  })
  @operationId("resumeDashboardPublication")
  resumeDashboardPublication(@path workspace: string, @path publication: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DashboardPublicationResponse> | CommonErrors;

  @summary("Rotate a dashboard publication public ID")
  @route("/{publication}/rotate")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PUBLICATIONS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "dashboard_publication.public_id_rotated", guarantee: "best-effort" },
    targetParameter: "workspace",
  })
  @operationId("rotateDashboardPublication")
  rotateDashboardPublication(@path workspace: string, @path publication: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<DashboardPublicationResponse> | CommonErrors;
}
