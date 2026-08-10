using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Refresh;

model RefreshRunCreateRequest {
  pipelineId: string;
  retryOf?: string;
}

model RefreshRunResponse {
  id: string;
  workspaceId: string;
  pipelineId: string;
  semanticModel: string;
  principalId?: string;
  principalDisplayName?: string;
  trigger: string;
  retryOf?: string;
  status: string;
  error?: string;
  createdAt: utcDateTime;
  startedAt?: utcDateTime;
  finishedAt?: utcDateTime;
}

model RefreshRunListResponse {
  items: RefreshRunResponse[];
  page: PageInfo;
}

alias ListRefreshRunsOK = OkJson<RefreshRunListResponse>;
alias CreateRefreshRunAccepted = AcceptedJson<RefreshRunResponse>;
alias GetRefreshRunOK = OkJson<RefreshRunResponse>;

@route("/workspaces/{workspace}/refresh-runs")
@tag("Refresh Runs")
interface RefreshRuns {
  @summary("List refresh runs")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
  @operationId("listRefreshRuns")
  listRefreshRuns(@path workspace: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListRefreshRunsOK | CommonErrors;

  @summary("Create a refresh run")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "refresh.queued", guarantee: "best-effort" },
  })
  @operationId("createRefreshRun")
  createRefreshRun(@path workspace: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RefreshRunCreateRequest): CreateRefreshRunAccepted | CommonErrors;

  @summary("Get a refresh run")
  @route("/{run}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
  @operationId("getRefreshRun")
  getRefreshRun(@path workspace: string, @path run: string): GetRefreshRunOK | CommonErrors;

  @summary("List or stream refresh run events")
  @route("/{run}/events")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
  @operationId("listRefreshRunEvents")
  listRefreshRunEvents(@path workspace: string, @path run: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;

  @summary("Cancel a refresh run")
  @route("/{run}/cancel")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "refresh.cancelled", guarantee: "best-effort" },
    targetParameter: "workspace",
  })
  @operationId("cancelRefreshRun")
  cancelRefreshRun(@path workspace: string, @path run: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<RefreshRunResponse> | CommonErrors;
}
