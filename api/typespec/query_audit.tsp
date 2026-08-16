using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Analytics;

model QueryEventResponse {
  id: string;
  projectId: ResourceId;
  principalId?: string;
  surface: string;
  operation: string;
  queryKind: string;
  modelId: ResourceId;
  resourceKind: AuditTargetKind;
  resourceId: ResourceId;
  requestId: string;
  correlationId: string;
  status: string;
  durationMs: int64;
  rowsReturned: int32;
  bytesEstimate: int64;
  error?: string;
  sql?: string;
  planText?: string;
  query: Record<unknown>;
  createdAt: utcDateTime;
}

model QueryEventListResponse {
  items: QueryEventResponse[];
  page: PageInfo;
}

alias ListQueryEventsOK = OkJson<QueryEventListResponse>;

@tag("Audit")
@summary("List project query audit events")
@route("/projects/{project}/query-events")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op listQueryEvents(@path project: ResourceId, @query principal?: string, @query surface?: string, @query operation?: string, @query kind?: string, @query modelId?: ResourceId, @query resourceKind?: AuditTargetKind, @query resourceId?: ResourceId, @query status?: string, @query search?: string, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListQueryEventsOK | CommonErrors;
