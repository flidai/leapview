using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Refresh;

model RefreshRunCreateRequest {
  pipelineId: ResourceId;
  retryOf?: string;
}

model RefreshRunResponse {
  id: string;
  identity: RefreshServingIdentity;
  pipelineId: ResourceId;
  semanticModel: ResourceId;
  principalId?: string;
  principalDisplayName?: string;
  invocationSource: string;
  matchingScheduleIds?: string[];
  nominalTime?: utcDateTime;
  planDigest: string;
  materializationScope: string[];
  retryOf?: string;
  status: string;
  error?: string;
  createdAt: utcDateTime;
  startedAt?: utcDateTime;
  finishedAt?: utcDateTime;
}

model RefreshServingIdentity {
  projectId: ResourceId;
  environment: string;
  generationId: ResourceId;
}

model RefreshRunListResponse {
  items: RefreshRunResponse[];
  page: PageInfo;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model RefreshQueuedAuditPayload {
  @apigen.auditInternal
  id: string;
  @apigen.auditInternal
  pipelineId: ResourceId;
  @apigen.auditInternal
  semanticModel: ResourceId;
  @apigen.auditPublic
  invocationSource: string;
  @apigen.auditPublic
  matchingScheduleIds: string[];
  @apigen.auditInternal
  planDigest: string;
  @apigen.auditInternal
  retryOf: string;
  @apigen.auditPublic
  status: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model RefreshCancelledAuditPayload {
  @apigen.auditInternal
  id: string;
  @apigen.auditInternal
  pipelineId: ResourceId;
  @apigen.auditPublic
  status: string;
  @apigen.auditPublic
  invocationSource: string;
  @apigen.auditPublic
  matchingScheduleIds: string[];
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 422, code: "INVALID_REFRESH_RUN", publicDetail: "The refresh run request is invalid." })
model InvalidRefreshRunFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "REFRESH_RUN_CONFLICT", publicDetail: "The refresh run conflicts with its current state." })
model RefreshRunConflictFailure {}

@apigen.failureDefinition(#{ kind: "unavailable", statusCode: 503, code: "REFRESH_SERVICE_UNAVAILABLE", publicDetail: "Refresh service is unavailable." })
model RefreshServiceUnavailableFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "REFRESH_RUN_NOT_FOUND", publicDetail: "Refresh run not found." })
model RefreshRunNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "REFRESH_PIPELINE_NOT_FOUND", publicDetail: "Refresh pipeline not found." })
model RefreshPipelineNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "not_cancellable", statusCode: 409, code: "REFRESH_RUN_NOT_CANCELLABLE", publicDetail: "Only queued refresh runs can be cancelled." })
model RefreshRunNotCancellableFailure {}

alias ListRefreshRunsOK = OkJson<RefreshRunListResponse>;
alias CreateRefreshRunAccepted = AcceptedJson<RefreshRunResponse>;
alias GetRefreshRunOK = OkJson<RefreshRunResponse>;

@route("/projects/{project}/refresh-runs")
@tag("Refresh Runs")
@apigen.commandDefaults(#{ guarantee: "best-effort" })
interface RefreshRuns {
  @summary("List refresh runs")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listRefreshRuns(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): ListRefreshRunsOK | CommonErrors;

  @summary("Create a refresh run")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("refresh.run")
  @apigen.failsWith(InvalidRefreshRunFailure)
  @apigen.failsWith(RefreshRunConflictFailure)
  @apigen.failsWith(RefreshPipelineNotFoundFailure)
  @apigen.failsWith(RefreshServiceUnavailableFailure)
  @apigen.asyncExecution(RefreshRuns.getRefreshRun, RefreshRuns.listRefreshRunEvents, #{
    guarantee: "transactional",
    jobKind: "refresh_pipeline",
    resourceKind: "refresh",
    initialEvent: "refresh.queued",
    initialState: "queued",
    cancellation: "supported",
  })
  @apigen.command(#{ auditAction: "refresh.queued" })
  @apigen.auditPayload(RefreshQueuedAuditPayload)
  createRefreshRun(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RefreshRunCreateRequest): CreateRefreshRunAccepted | CommonErrors;

  @summary("Get a refresh run")
  @route("/{run}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  getRefreshRun(@path project: ResourceId, @path run: string): GetRefreshRunOK | CommonErrors;

  @summary("List or stream refresh run events")
  @route("/{run}/events")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listRefreshRunEvents(@path project: ResourceId, @path run: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;

  @summary("Cancel a refresh run")
  @route("/{run}/cancel")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("refresh.cancel")
  @apigen.failsWith(RefreshRunNotFoundFailure)
  @apigen.failsWith(RefreshRunNotCancellableFailure)
  @apigen.failsWith(RefreshServiceUnavailableFailure)
  @apigen.auditPayload(RefreshCancelledAuditPayload)
  @apigen.command(#{ auditAction: "refresh.cancelled" })
  cancelRefreshRun(@path @apigen.target project: ResourceId, @path run: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<RefreshRunResponse> | CommonErrors;
}
