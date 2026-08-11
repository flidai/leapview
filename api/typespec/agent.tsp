using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Agent;

model AgentCommandAuditPayload {
  @apigen.sensitivity("internal")
  operationId: string;

  @apigen.sensitivity("internal")
  workspaceId: string;

  @apigen.sensitivity("public")
  targetType: string;

  @apigen.sensitivity("internal")
  targetId: string;

  @apigen.sensitivity("public")
  surface: string;
}

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

alias ListAgentConversationsOK = OkJson<AgentConversationListResponse>;
alias CreateAgentConversationCreated = CreatedJson<AgentConversationResponse>;
alias GetAgentConversationOK = OkJson<AgentConversationResponse>;
alias UpdateAgentConversationOK = OkJson<AgentConversationResponse>;
alias ListAgentMessagesOK = OkJson<AgentMessageListResponse>;
alias ListAgentRunsOK = OkJson<AgentRunListResponse>;
alias GetAgentRunOK = OkJson<AgentRunResponse>;
alias ListAgentEventsOK = OkJson<AsyncEventListResponse>;

@tag("Agent")
@doc("Agent conversation and run lifecycle. GET operations and event streams are queries; create, update, archive, start, and cancel operations mutate durable agent state.")
@route("/agent/conversations")
interface AgentConversations {
  @summary("List agent conversations")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_AGENT" })
  @apigen.cli(#{ command: #["agent", "conversations"], output: #{ mode: "raw" } })
  @operationId("listAgentConversations")
  listAgentConversations(@query limit?: ListLimit, @query pageToken?: PageToken): ListAgentConversationsOK | CommonErrors;

  @summary("Create an agent conversation")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "USE_AGENT" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "agent.conversation.created", guarantee: "best-effort" },
    failures: #[
      #{ kind: "unavailable", statusCode: 503, code: "AGENT_SERVICE_UNAVAILABLE", publicDetail: "Agent service is unavailable." },
    ],
    additionalExposures: #["ui"],
  })
  @apigen.auditPayload(AgentCommandAuditPayload, #{ schemaVersion: 1, retention: "security" })
  @operationId("createAgentConversation")
  createAgentConversation(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: AgentConversationCreateRequest): CreateAgentConversationCreated | CommonErrors;

  @summary("Archive an agent conversation")
  @route("/{conversation}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "USE_AGENT" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "agent.conversation.archived", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "AGENT_CONVERSATION_NOT_FOUND", publicDetail: "Agent conversation not found." },
    ],
  })
  @apigen.auditPayload(AgentCommandAuditPayload, #{ schemaVersion: 1, retention: "security" })
  @operationId("archiveAgentConversation")
  archiveAgentConversation(@path conversation: string): EmptyResponse | CommonErrors;

  @summary("Get an agent conversation")
  @route("/{conversation}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_AGENT" })
  @operationId("getAgentConversation")
  getAgentConversation(@path conversation: string): GetAgentConversationOK | CommonErrors;

  @summary("Update an agent conversation")
  @route("/{conversation}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "USE_AGENT" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "agent.conversation.updated", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "AGENT_CONVERSATION_NOT_FOUND", publicDetail: "Agent conversation not found." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_AGENT_CONVERSATION", publicDetail: "The agent conversation request is invalid." },
    ],
  })
  @apigen.auditPayload(AgentCommandAuditPayload, #{ schemaVersion: 1, retention: "security" })
  @operationId("updateAgentConversation")
  updateAgentConversation(@path conversation: string, @header("If-Match") ifMatch: string, @body body: AgentConversationPatchRequest): UpdateAgentConversationOK | CommonErrors;

  @summary("List agent messages")
  @route("/{conversation}/messages")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_AGENT" })
  @operationId("listAgentMessages")
  listAgentMessages(@path conversation: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListAgentMessagesOK | CommonErrors;

  @summary("List agent runs")
  @route("/{conversation}/runs")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_AGENT" })
  @operationId("listAgentRuns")
  listAgentRuns(@path conversation: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListAgentRunsOK | CommonErrors;

  @summary("Get an agent run")
  @route("/{conversation}/runs/{run}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_AGENT" })
  @operationId("getAgentRun")
  getAgentRun(@path conversation: string, @path run: string): GetAgentRunOK | CommonErrors;

  @summary("List agent run events")
  @route("/{conversation}/runs/{run}/events")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_AGENT" })
  @operationId("listAgentEvents")
  listAgentEvents(@path conversation: string, @path run: string, @header accept?: string, @header("Last-Event-ID") lastEventId?: string, @query limit?: ListLimit, @query pageToken?: PageToken): EventHistoryResponse<AsyncEventListResponse> | CommonErrors;

  @summary("Create an asynchronous agent run")
  @route("/{conversation}/runs")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "USE_AGENT" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "agent.run.created", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "AGENT_CONVERSATION_NOT_FOUND", publicDetail: "Agent conversation not found." },
      #{ kind: "conflict", statusCode: 409, code: "AGENT_RUN_CONFLICT", publicDetail: "The agent conversation already has an active run." },
      #{ kind: "invalid", statusCode: 422, code: "INVALID_AGENT_RUN", publicDetail: "The agent run request is invalid." },
      #{ kind: "unavailable", statusCode: 503, code: "AGENT_SERVICE_UNAVAILABLE", publicDetail: "Agent service is unavailable." },
    ],
    additionalExposures: #["ui"],
    execution: #{
      mode: "async",
      guarantee: "transactional",
      jobKind: "agent.run",
      resourceKind: "agent_run",
      initialEvent: "agent_run.queued",
      initialState: "running",
      statusOperation: "getAgentRun",
      eventsOperation: "listAgentEvents",
      cancellation: "supported",
    },
  })
  @apigen.auditPayload(AgentCommandAuditPayload, #{ schemaVersion: 1, retention: "security" })
  @operationId("createAgentRun")
  createAgentRun(@path conversation: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: AgentRunCreateRequest): AcceptedJson<AgentRunResponse> | CommonErrors;

  @summary("Cancel an agent run")
  @doc("Cancels an existing run and its queued work synchronously. The 202 response reports the resulting run state; this operation does not create a new asynchronous execution.")
  @route("/{conversation}/runs/{run}/cancel")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "USE_AGENT" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "agent.run.cancelled", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "AGENT_RUN_NOT_FOUND", publicDetail: "Agent run not found." },
      #{ kind: "not_cancellable", statusCode: 409, code: "AGENT_RUN_NOT_CANCELLABLE", publicDetail: "The agent run cannot be cancelled in its current state." },
      #{ kind: "unavailable", statusCode: 503, code: "AGENT_SERVICE_UNAVAILABLE", publicDetail: "Agent service is unavailable." },
    ],
    targetParameter: "conversation",
  })
  @apigen.auditPayload(AgentCommandAuditPayload, #{ schemaVersion: 1, retention: "security" })
  @operationId("cancelAgentRun")
  cancelAgentRun(@path conversation: string, @path run: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<AgentRunResponse> | CommonErrors;
}
