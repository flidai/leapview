using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Access;

model AuditEventResponse {
  id: string;
  projectId?: ResourceId;
  principalId?: string;
  action: string;
  targetType: string;
  targetId: string;
  privilege?: string;
  status?: string;
  requestId?: string;
  correlationId?: string;
  metadata: Record<unknown>;
  createdAt: utcDateTime;
}

model AuditEventListResponse {
  items: AuditEventResponse[];
  page: PageInfo;
}

alias ListAuditEventsOK = OkJson<AuditEventListResponse>;

@tag("Audit")
@summary("List project audit events")
@route("/projects/{project}/audit-events")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
op listAuditEvents(@path project: ResourceId, @query actor?: string, @query action?: string, @query targetType?: string, @query targetId?: string, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListAuditEventsOK | CommonErrors;

@tag("Audit")
@summary("List product audit events")
@route("/audit-events")
@get
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
@extension("x-leapview-object-scope", "platform")
op listPlatformAuditEvents(@query project?: ResourceId, @query actor?: string, @query action?: string, @query targetType?: string, @query targetId?: string, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListAuditEventsOK | CommonErrors;
