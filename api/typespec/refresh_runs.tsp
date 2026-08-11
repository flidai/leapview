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

model RefreshQueuedAuditPayload {
  @apigen.sensitivity("internal")
  id: string;
  @apigen.sensitivity("internal")
  workspaceId: string;
  @apigen.sensitivity("internal")
  pipelineId: string;
  @apigen.sensitivity("internal")
  semanticModel: string;
  @apigen.sensitivity("public")
  trigger: string;
  @apigen.sensitivity("internal")
  retryOf: string;
  @apigen.sensitivity("public")
  status: string;
}

model RefreshCancelledAuditPayload {
  @apigen.sensitivity("internal")
  id: string;
  @apigen.sensitivity("internal")
  workspaceId: string;
  @apigen.sensitivity("internal")
  pipelineId: string;
  @apigen.sensitivity("public")
  status: string;
  @apigen.sensitivity("public")
  trigger: string;
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
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "INVALID_REFRESH_RUN", publicDetail: "The refresh run request is invalid." },
      #{ kind: "conflict", statusCode: 409, code: "REFRESH_RUN_CONFLICT", publicDetail: "The refresh run conflicts with its current state." },
      #{ kind: "forbidden", statusCode: 403, code: "REFRESH_RUN_FORBIDDEN", publicDetail: "The refresh run is not permitted." },
      #{ kind: "unavailable", statusCode: 503, code: "REFRESH_SERVICE_UNAVAILABLE", publicDetail: "Refresh service is unavailable." },
    ],
    execution: #{
      mode: "async",
      guarantee: "transactional",
      jobKind: "refresh_pipeline",
      resourceKind: "refresh",
      initialEvent: "refresh.queued",
      initialState: "queued",
      statusOperation: "getRefreshRun",
      eventsOperation: "listRefreshRunEvents",
      cancellation: "supported",
    },
  })
  @apigen.auditPayload(RefreshQueuedAuditPayload, #{ schemaVersion: 1, retention: "security" })
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
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "REFRESH_RUN_NOT_FOUND", publicDetail: "Refresh run not found." },
      #{ kind: "forbidden", statusCode: 403, code: "REFRESH_RUN_FORBIDDEN", publicDetail: "The refresh run is not permitted." },
      #{ kind: "not_cancellable", statusCode: 409, code: "REFRESH_RUN_NOT_CANCELLABLE", publicDetail: "Only queued refresh runs can be cancelled." },
      #{ kind: "unavailable", statusCode: 503, code: "REFRESH_SERVICE_UNAVAILABLE", publicDetail: "Refresh service is unavailable." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(RefreshCancelledAuditPayload, #{ schemaVersion: 1, retention: "security" })
  @operationId("cancelRefreshRun")
  cancelRefreshRun(@path workspace: string, @path run: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<RefreshRunResponse> | CommonErrors;
}
