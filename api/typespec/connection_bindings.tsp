using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Analytics;

@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z_][A-Za-z0-9_]{0,127}$")
scalar LogicalConnectionId extends string;

@minLength(1)
@maxLength(128)
@pattern("^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$")
scalar TargetConnectionIdentifier extends string;

@minValue(1)
scalar TargetConnectionRevision extends int64;

enum TargetConnectionAuthenticationMode {
  none;
  externalBundle: "external_bundle";
  workloadIdentity: "workload_identity";
}

enum TargetConnectionHealth {
  pending;
  healthy;
  degraded;
  disabled;
}

model TargetConnectionEndpoint {
  host?: string;
  port?: int32;
  database?: string;
  objectScope?: string;
  sourceIdentity?: string;
  tlsMode?: string;
  options?: Record<string>;
}

@doc("A provider-side reference only. LeapView never returns or accepts the referenced secret value.")
model TargetConnectionCredentialReference {
  projectId: string;
  environment: string;
  secretPath: string;
  secretKey: string;
}

model TargetConnectionConfiguration {
  connectorKind: TargetConnectionIdentifier;
  authenticationMode: TargetConnectionAuthenticationMode;
  endpoint: TargetConnectionEndpoint;
  credentialReference?: TargetConnectionCredentialReference;
}

model TargetConnectionBindingCreateRequest {
  id: TargetConnectionIdentifier;
  logicalConnection: LogicalConnectionId;
  configuration: TargetConnectionConfiguration;
  enabled: boolean;
}

model TargetConnectionBindingUpdateRequest {
  configuration: TargetConnectionConfiguration;
  expectedRevision: TargetConnectionRevision;
  confirmationToken?: string;
}

model TargetConnectionBindingPlanRequest {
  configuration: TargetConnectionConfiguration;
}

model TargetConnectionDependency {
  kind: string;
  id: string;
  label: string;
}

model TargetConnectionBindingChangePlanResponse {
  bindingId: string;
  expectedRevision: TargetConnectionRevision;
  requiresConfirmation: boolean;
  confirmationToken?: string;
  dependencies: TargetConnectionDependency[];
}

@doc("Target binding metadata. Credential references identify provider objects but never contain secret values.")
model TargetConnectionBindingResponse {
  id: string;
  targetId: string;
  logicalConnection: LogicalConnectionId;
  connectorKind: TargetConnectionIdentifier;
  authenticationMode: TargetConnectionAuthenticationMode;
  workspaceId: string;
  environment: TargetConnectionIdentifier;
  endpoint: TargetConnectionEndpoint;
  credentialReference?: TargetConnectionCredentialReference;
  enabled: boolean;
  validatedVersion?: string;
  health: TargetConnectionHealth;
  lastValidatedAt?: utcDateTime;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  revision: TargetConnectionRevision;
}

model TargetConnectionBindingListResponse {
  items: TargetConnectionBindingResponse[];
}

@doc("Redacted runtime health. Diagnostic codes are bounded LeapView codes, never raw provider errors.")
model TargetConnectionBindingHealthResponse {
  bindingId: string;
  targetId: string;
  logicalConnection: LogicalConnectionId;
  connectorKind: TargetConnectionIdentifier;
  workspaceId: string;
  environment: TargetConnectionIdentifier;
  bindingRevision: TargetConnectionRevision;
  validatedVersion?: string;
  health: TargetConnectionHealth;
  diagnosticCode?: string;
  lastAttemptAt?: utcDateTime;
  lastValidatedAt?: utcDateTime;
  staleAgeSeconds?: int64;
  hasActivePool: boolean;
}

@route("/workspaces/{workspace}/targets/{target}/environments/{environment}/connection-bindings")
@tag("Connections")
interface TargetConnectionBindings {
  @summary("List target connection bindings")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_CONNECTION_METADATA" })
  @operationId("listTargetConnectionBindings")
  listTargetConnectionBindings(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
  ): OkJson<TargetConnectionBindingListResponse> | CommonErrors;

  @summary("Create a target connection binding")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_CONNECTION_METADATA" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "connection.binding.created", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "INVALID_CONNECTION_BINDING", publicDetail: "Connection binding is invalid." },
      #{ kind: "unauthorized", statusCode: 403, code: "FORBIDDEN", publicDetail: "Connection operation is forbidden." },
      #{ kind: "not_found", statusCode: 404, code: "CONNECTION_BINDING_NOT_FOUND", publicDetail: "Connection binding was not found." },
      #{ kind: "conflict", statusCode: 409, code: "CONNECTION_BINDING_CONFLICT", publicDetail: "Connection binding changed concurrently or is incompatible." },
      #{ kind: "credential_invalid", statusCode: 422, code: "CONNECTION_TEST_FAILED", publicDetail: "Connection validation failed." },
      #{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." },
      #{ kind: "provider_unavailable", statusCode: 503, code: "CONNECTION_PROVIDER_UNAVAILABLE", publicDetail: "Connection provider is unavailable." },
    ],
    targetParameter: "workspace",
  })
  @operationId("createTargetConnectionBinding")
  createTargetConnectionBinding(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
    @body body: TargetConnectionBindingCreateRequest,
  ): CreatedJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Get target connection binding metadata")
  @route("/{connection}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_CONNECTION_METADATA" })
  @operationId("getTargetConnectionBinding")
  getTargetConnectionBinding(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Plan a dependency-aware target connection change")
  @doc("Query classification: computes dependencies and a confirmation token without mutating durable binding or runtime state.")
  @route("/{connection}/plan")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_CONNECTION_METADATA" })
  @apigen.query
  @operationId("planTargetConnectionBindingChange")
  planTargetConnectionBindingChange(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
    @body body: TargetConnectionBindingPlanRequest,
  ): OkJson<TargetConnectionBindingChangePlanResponse> | CommonErrors;

  @summary("Update target connection binding metadata")
  @route("/{connection}")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_CONNECTION_METADATA" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "connection.binding.updated", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "INVALID_CONNECTION_BINDING", publicDetail: "Connection binding is invalid." },
      #{ kind: "unauthorized", statusCode: 403, code: "FORBIDDEN", publicDetail: "Connection operation is forbidden." },
      #{ kind: "not_found", statusCode: 404, code: "CONNECTION_BINDING_NOT_FOUND", publicDetail: "Connection binding was not found." },
      #{ kind: "conflict", statusCode: 409, code: "CONNECTION_BINDING_CONFLICT", publicDetail: "Connection binding changed concurrently or is incompatible." },
      #{ kind: "disabled", statusCode: 409, code: "CONNECTION_BINDING_DISABLED", publicDetail: "Connection binding is disabled." },
      #{ kind: "confirmation_required", statusCode: 412, code: "CONFIRMATION_REQUIRED", publicDetail: "Dependency confirmation is required." },
      #{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." },
      #{ kind: "provider_unavailable", statusCode: 503, code: "CONNECTION_PROVIDER_UNAVAILABLE", publicDetail: "Connection provider is unavailable." },
    ],
    targetParameter: "workspace",
  })
  @operationId("updateTargetConnectionBinding")
  updateTargetConnectionBinding(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
    @body body: TargetConnectionBindingUpdateRequest,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Test a target connection through the candidate runtime path")
  @doc("Command classification: testing can persist validated health/version state and promote the replacement runtime pool.")
  @route("/{connection}/test")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "TEST_CONNECTION" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "credential.test.requested", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "INVALID_CONNECTION_BINDING", publicDetail: "Connection binding is invalid." },
      #{ kind: "unauthorized", statusCode: 403, code: "FORBIDDEN", publicDetail: "Connection operation is forbidden." },
      #{ kind: "not_found", statusCode: 404, code: "CONNECTION_BINDING_NOT_FOUND", publicDetail: "Connection binding was not found." },
      #{ kind: "conflict", statusCode: 409, code: "CONNECTION_BINDING_CONFLICT", publicDetail: "Connection binding changed concurrently or is incompatible." },
      #{ kind: "disabled", statusCode: 409, code: "CONNECTION_BINDING_DISABLED", publicDetail: "Connection binding is disabled." },
      #{ kind: "credential_invalid", statusCode: 422, code: "CONNECTION_TEST_FAILED", publicDetail: "Connection validation failed." },
      #{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." },
      #{ kind: "provider_unavailable", statusCode: 503, code: "CONNECTION_PROVIDER_UNAVAILABLE", publicDetail: "Connection provider is unavailable." },
    ],
    targetParameter: "workspace",
  })
  @operationId("testTargetConnectionBinding")
  testTargetConnectionBinding(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingHealthResponse> | CommonErrors;

  @summary("Refresh credentials and replace the validated target pool")
  @route("/{connection}/refresh")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "TEST_CONNECTION" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "credential.refresh.requested", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "INVALID_CONNECTION_BINDING", publicDetail: "Connection binding is invalid." },
      #{ kind: "unauthorized", statusCode: 403, code: "FORBIDDEN", publicDetail: "Connection operation is forbidden." },
      #{ kind: "not_found", statusCode: 404, code: "CONNECTION_BINDING_NOT_FOUND", publicDetail: "Connection binding was not found." },
      #{ kind: "conflict", statusCode: 409, code: "CONNECTION_BINDING_CONFLICT", publicDetail: "Connection binding changed concurrently or is incompatible." },
      #{ kind: "disabled", statusCode: 409, code: "CONNECTION_BINDING_DISABLED", publicDetail: "Connection binding is disabled." },
      #{ kind: "credential_invalid", statusCode: 422, code: "CONNECTION_TEST_FAILED", publicDetail: "Connection validation failed." },
      #{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." },
      #{ kind: "provider_unavailable", statusCode: 503, code: "CONNECTION_PROVIDER_UNAVAILABLE", publicDetail: "Connection provider is unavailable." },
    ],
    targetParameter: "workspace",
  })
  @operationId("refreshTargetConnectionBinding")
  refreshTargetConnectionBinding(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingHealthResponse> | CommonErrors;

  @summary("Enable a target connection binding")
  @route("/{connection}/enable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_CONNECTION_METADATA" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "connection.binding.enabled", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "INVALID_CONNECTION_BINDING", publicDetail: "Connection binding is invalid." },
      #{ kind: "unauthorized", statusCode: 403, code: "FORBIDDEN", publicDetail: "Connection operation is forbidden." },
      #{ kind: "not_found", statusCode: 404, code: "CONNECTION_BINDING_NOT_FOUND", publicDetail: "Connection binding was not found." },
      #{ kind: "conflict", statusCode: 409, code: "CONNECTION_BINDING_CONFLICT", publicDetail: "Connection binding changed concurrently or is incompatible." },
      #{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." },
      #{ kind: "provider_unavailable", statusCode: 503, code: "CONNECTION_PROVIDER_UNAVAILABLE", publicDetail: "Connection provider is unavailable." },
    ],
    targetParameter: "workspace",
  })
  @operationId("enableTargetConnectionBinding")
  enableTargetConnectionBinding(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Disable a target connection binding")
  @route("/{connection}/disable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_CONNECTION_METADATA" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "connection.binding.disabled", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "INVALID_CONNECTION_BINDING", publicDetail: "Connection binding is invalid." },
      #{ kind: "unauthorized", statusCode: 403, code: "FORBIDDEN", publicDetail: "Connection operation is forbidden." },
      #{ kind: "not_found", statusCode: 404, code: "CONNECTION_BINDING_NOT_FOUND", publicDetail: "Connection binding was not found." },
      #{ kind: "conflict", statusCode: 409, code: "CONNECTION_BINDING_CONFLICT", publicDetail: "Connection binding changed concurrently or is incompatible." },
      #{ kind: "disabled", statusCode: 409, code: "CONNECTION_BINDING_DISABLED", publicDetail: "Connection binding is disabled." },
      #{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." },
      #{ kind: "provider_unavailable", statusCode: 503, code: "CONNECTION_PROVIDER_UNAVAILABLE", publicDetail: "Connection provider is unavailable." },
    ],
    targetParameter: "workspace",
  })
  @operationId("disableTargetConnectionBinding")
  disableTargetConnectionBinding(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Get redacted target connection runtime health")
  @route("/{connection}/health")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "VIEW_CONNECTION_HEALTH" })
  @operationId("getTargetConnectionBindingHealth")
  getTargetConnectionBindingHealth(
    @path workspace: string,
    @path target: TargetConnectionIdentifier,
    @path environment: TargetConnectionIdentifier,
    @path connection: LogicalConnectionId,
  ): OkJson<TargetConnectionBindingHealthResponse> | CommonErrors;
}
