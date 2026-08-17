using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Agent;

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model AgentCommandAuditPayload {
  @apigen.auditInternal
  operationId: string;

  @apigen.auditPublic
  resourceKind: AuditTargetKind;

  @apigen.auditInternal
  resourceId: ResourceId;

  @apigen.auditPublic
  surface: string;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 422, code: "INVALID_AGENT_CONFIG", publicDetail: "The agent configuration is invalid." })
model InvalidAgentConfigFailure {}

@apigen.failureDefinition(#{ kind: "unavailable", statusCode: 503, code: "AGENT_CONFIG_UNAVAILABLE", publicDetail: "Agent configuration is unavailable." })
model AgentConfigUnavailableFailure {}

@apigen.failureDefinition(#{ kind: "unavailable", statusCode: 503, code: "AGENT_SERVICE_UNAVAILABLE", publicDetail: "Agent service is unavailable." })
model AgentServiceUnavailableFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "AGENT_CONVERSATION_NOT_FOUND", publicDetail: "Agent conversation not found." })
model AgentConversationNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 422, code: "INVALID_AGENT_CONVERSATION", publicDetail: "The agent conversation request is invalid." })
model InvalidAgentConversationFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "AGENT_RUN_CONFLICT", publicDetail: "The agent conversation already has an active run." })
model AgentRunConflictFailure {}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 422, code: "INVALID_AGENT_RUN", publicDetail: "The agent run request is invalid." })
model InvalidAgentRunFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "AGENT_RUN_NOT_FOUND", publicDetail: "Agent run not found." })
model AgentRunNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "not_cancellable", statusCode: 409, code: "AGENT_RUN_NOT_CANCELLABLE", publicDetail: "The agent run cannot be cancelled in its current state." })
model AgentRunNotCancellableFailure {}

model AgentConversationCreateRequest {
  title?: string;
}

model AgentConversationPatchRequest {
  title?: string;
}

model AgentConversationResponse {
  id: string;
  principalId: string;
  title: string;
  status: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  archivedAt?: utcDateTime;
  messageCount?: int32;
  lastMessageText?: string;
  titlePending?: boolean;
}

model AgentConversationListResponse {
  items: AgentConversationResponse[];
  page: PageInfo;
}

model AgentRunResponse {
  id: string;
  conversationId: string;
  principalId: string;
  status: string;
  "model"?: string;
  stopReason?: string;
  createdAt: utcDateTime;
  startedAt?: utcDateTime;
  completedAt?: utcDateTime;
  error?: string;
}

model AgentRunListResponse {
  items: AgentRunResponse[];
  page: PageInfo;
}

model AgentMessageResponse {
  id: string;
  runId?: string;
  seq: int64;
  role: string;
  contentText?: string;
  content?: Record<unknown>;
  toolCallId?: string;
  toolName?: string;
  isError?: boolean;
  createdAt: utcDateTime;
}

model AgentMessageListResponse {
  items: AgentMessageResponse[];
  page: PageInfo;
}

model AgentRunCreateRequest {
  input: string;
  correlationId?: string;
}

model AgentConfigUpdateRequest {
  systemPrompt: string;
}

model AgentConfigResponse {
  systemPrompt: string;
}

alias ListAgentConversationsOK = OkJson<AgentConversationListResponse>;
alias CreateAgentConversationCreated = CreatedJson<AgentConversationResponse>;
alias GetAgentConversationOK = OkJson<AgentConversationResponse>;
alias UpdateAgentConversationOK = OkJson<AgentConversationResponse>;
alias ListAgentMessagesOK = OkJson<AgentMessageListResponse>;
alias ListAgentRunsOK = OkJson<AgentRunListResponse>;
alias GetAgentRunOK = OkJson<AgentRunResponse>;
alias ListAgentEventsOK = OkJson<AsyncEventListResponse>;
alias UpdateAgentConfigOK = OkJson<AgentConfigResponse>;
alias GetAgentConfigOK = OkJson<AgentConfigResponse>;

@tag("Agent")
@doc("Platform-wide agent configuration. The update command is shared by API and administration UI invocations.")
@route("/agent/config")
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "platform")
@apigen.commandDefaults(#{ guarantee: "best-effort" })
@apigen.auditPayload(AgentCommandAuditPayload)
interface AgentConfiguration {
  @summary("Get the agent configuration")
  @get
  getAgentConfig(): GetAgentConfigOK | CommonErrors;

  @summary("Update the agent configuration")
  @patch
  @apigen.ui("agent.config.update")
  @apigen.failsWith(InvalidAgentConfigFailure)
  @apigen.failsWith(AgentConfigUnavailableFailure)
  @apigen.command(#{ auditAction: "agent.config.updated" })
  updateAgentConfig(@header("If-Match") ifMatch: string, @body body: AgentConfigUpdateRequest): UpdateAgentConfigOK | CommonErrors;
}

@tag("Agent")
@doc("Agent conversation and run lifecycle. GET operations and event streams are queries; create, update, archive, start, and cancel operations mutate durable agent state.")
@route("/agent/conversations")
@apigen.commandDefaults(#{ guarantee: "best-effort" })
@apigen.auditPayload(AgentCommandAuditPayload)
interface AgentConversations {
  @summary("List agent conversations")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.cli(#{ command: #["agent", "conversations"], output: #{ mode: "raw" } })
  listAgentConversations(@query limit?: ListLimit, @query pageToken?: PageToken): ListAgentConversationsOK | CommonErrors;

  @summary("Create an agent conversation")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("agent.conversation.create")
  @apigen.failsWith(AgentServiceUnavailableFailure)
  @apigen.command(#{ auditAction: "agent.conversation.created" })
  createAgentConversation(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: AgentConversationCreateRequest): CreateAgentConversationCreated | CommonErrors;

  @summary("Archive an agent conversation")
  @route("/{conversation}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.failsWith(AgentConversationNotFoundFailure)
  @apigen.command(#{ auditAction: "agent.conversation.archived" })
  archiveAgentConversation(@path @apigen.target conversation: string): EmptyResponse | CommonErrors;

  @summary("Get an agent conversation")
  @route("/{conversation}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  getAgentConversation(@path conversation: string): GetAgentConversationOK | CommonErrors;

  @summary("Update an agent conversation")
  @route("/{conversation}")
  @patch
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.failsWith(AgentConversationNotFoundFailure)
  @apigen.failsWith(InvalidAgentConversationFailure)
  @apigen.command(#{ auditAction: "agent.conversation.updated" })
  updateAgentConversation(@path @apigen.target conversation: string, @header("If-Match") ifMatch: string, @body body: AgentConversationPatchRequest): UpdateAgentConversationOK | CommonErrors;

  @summary("List agent messages")
  @route("/{conversation}/messages")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listAgentMessages(@path conversation: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListAgentMessagesOK | CommonErrors;

  @summary("List agent runs")
  @route("/{conversation}/runs")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listAgentRuns(@path conversation: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListAgentRunsOK | CommonErrors;

  @summary("Get an agent run")
  @route("/{conversation}/runs/{run}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  getAgentRun(@path conversation: string, @path run: string): GetAgentRunOK | CommonErrors;

  @summary("List agent run events")
  @route("/{conversation}/runs/{run}/events")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  listAgentEvents(@path conversation: string, @path run: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;

  @summary("Create an asynchronous agent run")
  @route("/{conversation}/runs")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.ui("agent.run.create")
  @apigen.failsWith(AgentConversationNotFoundFailure)
  @apigen.failsWith(AgentRunConflictFailure)
  @apigen.failsWith(InvalidAgentRunFailure)
  @apigen.failsWith(AgentServiceUnavailableFailure)
  @apigen.asyncExecution(AgentConversations.getAgentRun, AgentConversations.listAgentEvents, #{
    guarantee: "transactional",
    jobKind: "agent.run",
    resourceKind: "agent_run",
    initialEvent: "agent_run.queued",
    initialState: "running",
    cancellation: "supported",
  })
  @apigen.command(#{ auditAction: "agent.run.created" })
  createAgentRun(@path @apigen.target conversation: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: AgentRunCreateRequest): AcceptedJson<AgentRunResponse> | CommonErrors;

  @summary("Cancel an agent run")
  @doc("Cancels an existing run and its queued work synchronously. The 202 response reports the resulting run state; this operation does not create a new asynchronous execution.")
  @route("/{conversation}/runs/{run}/cancel")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.failsWith(AgentRunNotFoundFailure)
  @apigen.failsWith(AgentRunNotCancellableFailure)
  @apigen.failsWith(AgentServiceUnavailableFailure)
  @apigen.command(#{ auditAction: "agent.run.cancelled" })
  cancelAgentRun(@path @apigen.target conversation: string, @path run: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<AgentRunResponse> | CommonErrors;
}
