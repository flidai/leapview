using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Access;

model AuditEventResponse {
  id: string;
  workspaceId?: string;
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
@summary("List workspace audit events")
@route("/workspaces/{workspace}/audit-events")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_AUDIT" })
@operationId("listAuditEvents")
op listAuditEvents(@path workspace: string, @query actor?: string, @query action?: string, @query targetType?: string, @query targetId?: string, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListAuditEventsOK | CommonErrors;

@tag("Audit")
@summary("List product audit events")
@route("/audit-events")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_AUDIT" })
@extension("x-leapview-object-scope", "platform")
@operationId("listPlatformAuditEvents")
op listPlatformAuditEvents(@query workspace?: string, @query actor?: string, @query action?: string, @query targetType?: string, @query targetId?: string, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListAuditEventsOK | CommonErrors;
