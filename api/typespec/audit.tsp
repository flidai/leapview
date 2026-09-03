using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Access;

model AuditEventResponse {
  id: string;
  principalId?: string;
  action: string;
  resourceKind: AuditTargetKind;
  resourceId: ResourceId;
  capability?: Capability;
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
@summary("List instance audit events")
@route("/access/audit-events")
@get
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "platform")
op listAuditEvents(@query actor?: string, @query action?: string, @query resourceKind?: AuditTargetKind, @query resourceId?: ResourceId, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListAuditEventsOK | CommonErrors;

@tag("Audit")
@summary("List product audit events")
@route("/audit-events")
@get
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "platform")
op listPlatformAuditEvents(@query actor?: string, @query action?: string, @query resourceKind?: AuditTargetKind, @query resourceId?: ResourceId, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListAuditEventsOK | CommonErrors;
