using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Analytics;

model QueryEventResponse {
  id: string;
  workspaceId: string;
  principalId?: string;
  surface: string;
  operation: string;
  queryKind: string;
  modelId: string;
  target: string;
  objectType: string;
  objectId: string;
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
@summary("List workspace query audit events")
@route("/workspaces/{workspace}/query-events")
@get
@apigen.authz(#{ mode: "privilege", privilege: "VIEW_AUDIT" })
op listQueryEvents(@path workspace: string, @query principal?: string, @query surface?: string, @query operation?: string, @query kind?: string, @query modelId?: string, @query target?: string, @query status?: string, @query search?: string, @query from?: utcDateTime, @query to?: utcDateTime, @query limit?: ListLimit, @query pageToken?: PageToken): ListQueryEventsOK | CommonErrors;
