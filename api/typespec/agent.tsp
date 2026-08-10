using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Agent;

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
    additionalExposures: #["ui"],
  })
  @operationId("createAgentConversation")
  createAgentConversation(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: AgentConversationCreateRequest): CreateAgentConversationCreated | CommonErrors;

  @summary("Archive an agent conversation")
  @route("/{conversation}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "USE_AGENT" })
  @apigen.command(#{ audit: #{ required: true, successAction: "agent.conversation.archived", guarantee: "best-effort" } })
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
  @apigen.command(#{ audit: #{ required: true, successAction: "agent.conversation.updated", guarantee: "best-effort" } })
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
    additionalExposures: #["ui"],
  })
  @operationId("createAgentRun")
  createAgentRun(@path conversation: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: AgentRunCreateRequest): AcceptedJson<AgentRunResponse> | CommonErrors;

  @summary("Cancel an agent run")
  @route("/{conversation}/runs/{run}/cancel")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "USE_AGENT" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "agent.run.cancelled", guarantee: "best-effort" },
    targetParameter: "conversation",
  })
  @operationId("cancelAgentRun")
  cancelAgentRun(@path conversation: string, @path run: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): AcceptedJson<AgentRunResponse> | CommonErrors;
}
