using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Analytics;

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model TargetConnectionAdministrationAuditPayload {
  @apigen.auditInternal
  targetId: string;

  @apigen.auditInternal
  logicalConnection: ResourceId;

  @apigen.auditInternal
  revision: int64;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model TargetConnectionRotationAuditPayload {
  @apigen.auditPublic
  operation: string;

  @apigen.auditPublic
  outcome: string;

  @apigen.auditInternal
  providerVersion: string;

  @apigen.auditInternal
  diagnosticCode: string;

  @apigen.auditInternal
  targetId: string;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "INVALID_CONNECTION_BINDING", publicDetail: "Connection binding is invalid." })
model InvalidConnectionBindingFailure {}

@apigen.failureDefinition(#{ kind: "unauthorized", statusCode: 403, code: "FORBIDDEN", publicDetail: "Connection operation is forbidden." })
model ForbiddenConnectionOperationFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "CONNECTION_BINDING_NOT_FOUND", publicDetail: "Connection binding was not found." })
model ConnectionBindingNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "CONNECTION_BINDING_CONFLICT", publicDetail: "Connection binding changed concurrently or is incompatible." })
model ConnectionBindingConflictFailure {}

@apigen.failureDefinition(#{ kind: "credential_invalid", statusCode: 422, code: "CONNECTION_TEST_FAILED", publicDetail: "Connection validation failed." })
model ConnectionTestFailedFailure {}

@apigen.failureDefinition(#{ kind: "provider_unavailable", statusCode: 503, code: "CONNECTION_PROVIDER_UNAVAILABLE", publicDetail: "Connection provider is unavailable." })
model ConnectionProviderUnavailableFailure {}

@apigen.failureDefinition(#{ kind: "disabled", statusCode: 409, code: "CONNECTION_BINDING_DISABLED", publicDetail: "Connection binding is disabled." })
model ConnectionBindingDisabledFailure {}

@apigen.failureDefinition(#{ kind: "confirmation_required", statusCode: 412, code: "CONFIRMATION_REQUIRED", publicDetail: "Dependency confirmation is required." })
model ConfirmationRequiredFailure {}

alias LogicalConnectionId = ResourceId;

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
  projectId: ResourceId;
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

@doc("Target binding metadata. Credential references are write-only configuration and are never returned.")
model TargetConnectionBindingResponse {
  id: string;
  targetId: string;
  logicalConnection: LogicalConnectionId;
  connectorKind: TargetConnectionIdentifier;
  authenticationMode: TargetConnectionAuthenticationMode;
  environment: TargetConnectionIdentifier;
  endpoint: TargetConnectionEndpoint;
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

@route("/projects/{project}/targets/{target}/connection-bindings")
@tag("Connections")
@apigen.commandDefaults(#{ guarantee: "best-effort" })
interface TargetConnectionBindings {
  @summary("List target connection bindings")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  listTargetConnectionBindings(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
  ): OkJson<TargetConnectionBindingListResponse> | CommonErrors;

  @summary("Create a target connection binding")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.failsWith(InvalidConnectionBindingFailure)
  @apigen.failsWith(ForbiddenConnectionOperationFailure)
  @apigen.failsWith(ConnectionBindingNotFoundFailure)
  @apigen.failsWith(ConnectionBindingConflictFailure)
  @apigen.failsWith(ConnectionTestFailedFailure)
  @apigen.failsWith(ConnectionProviderUnavailableFailure)
  @apigen.ui("connection.binding.configure")
  @apigen.command(#{
    auditAction: "connection.binding.created",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." }],
  })
  @apigen.auditPayload(TargetConnectionAdministrationAuditPayload)
  createTargetConnectionBinding(
    @path @apigen.target project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
    @body body: TargetConnectionBindingCreateRequest,
  ): CreatedJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Get target connection binding metadata")
  @route("/{connection}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
  @extension("x-leapview-object-scope", "connection")
  getTargetConnectionBinding(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Plan a dependency-aware target connection change")
  @doc("Query classification: computes dependencies and a confirmation token without mutating durable binding or runtime state.")
  @route("/{connection}/plan")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.query
  planTargetConnectionBindingChange(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
    @body body: TargetConnectionBindingPlanRequest,
  ): OkJson<TargetConnectionBindingChangePlanResponse> | CommonErrors;

  @summary("Update target connection binding metadata")
  @route("/{connection}")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidConnectionBindingFailure)
  @apigen.failsWith(ForbiddenConnectionOperationFailure)
  @apigen.failsWith(ConnectionBindingNotFoundFailure)
  @apigen.failsWith(ConnectionBindingConflictFailure)
  @apigen.failsWith(ConnectionBindingDisabledFailure)
  @apigen.failsWith(ConfirmationRequiredFailure)
  @apigen.failsWith(ConnectionProviderUnavailableFailure)
  @apigen.ui("connection.binding.update")
  @apigen.command(#{
    auditAction: "connection.binding.updated",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." }],
  })
  @apigen.auditPayload(TargetConnectionAdministrationAuditPayload)
  updateTargetConnectionBinding(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
    @body body: TargetConnectionBindingUpdateRequest,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Test a target connection through the candidate runtime path")
  @doc("Command classification: testing can persist validated health/version state and promote the replacement runtime pool.")
  @route("/{connection}/test")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidConnectionBindingFailure)
  @apigen.failsWith(ForbiddenConnectionOperationFailure)
  @apigen.failsWith(ConnectionBindingNotFoundFailure)
  @apigen.failsWith(ConnectionBindingConflictFailure)
  @apigen.failsWith(ConnectionBindingDisabledFailure)
  @apigen.failsWith(ConnectionTestFailedFailure)
  @apigen.failsWith(ConnectionProviderUnavailableFailure)
  @apigen.ui("connection.binding.test")
  @apigen.command(#{
    auditAction: "credential.test.requested",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." }],
  })
  @apigen.auditPayload(TargetConnectionRotationAuditPayload)
  testTargetConnectionBinding(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingHealthResponse> | CommonErrors;

  @summary("Refresh credentials and replace the validated target pool")
  @route("/{connection}/refresh")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidConnectionBindingFailure)
  @apigen.failsWith(ForbiddenConnectionOperationFailure)
  @apigen.failsWith(ConnectionBindingNotFoundFailure)
  @apigen.failsWith(ConnectionBindingConflictFailure)
  @apigen.failsWith(ConnectionBindingDisabledFailure)
  @apigen.failsWith(ConnectionTestFailedFailure)
  @apigen.failsWith(ConnectionProviderUnavailableFailure)
  @apigen.ui("connection.binding.refresh")
  @apigen.command(#{
    auditAction: "credential.refresh.requested",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." }],
  })
  @apigen.auditPayload(TargetConnectionRotationAuditPayload)
  refreshTargetConnectionBinding(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingHealthResponse> | CommonErrors;

  @summary("Enable a target connection binding")
  @route("/{connection}/enable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidConnectionBindingFailure)
  @apigen.failsWith(ForbiddenConnectionOperationFailure)
  @apigen.failsWith(ConnectionBindingNotFoundFailure)
  @apigen.failsWith(ConnectionBindingConflictFailure)
  @apigen.failsWith(ConnectionProviderUnavailableFailure)
  @apigen.ui("connection.binding.enable")
  @apigen.command(#{
    auditAction: "connection.binding.enabled",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." }],
  })
  @apigen.auditPayload(TargetConnectionAdministrationAuditPayload)
  enableTargetConnectionBinding(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Disable a target connection binding")
  @route("/{connection}/disable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidConnectionBindingFailure)
  @apigen.failsWith(ForbiddenConnectionOperationFailure)
  @apigen.failsWith(ConnectionBindingNotFoundFailure)
  @apigen.failsWith(ConnectionBindingConflictFailure)
  @apigen.failsWith(ConnectionBindingDisabledFailure)
  @apigen.failsWith(ConnectionProviderUnavailableFailure)
  @apigen.ui("connection.binding.disable")
  @apigen.command(#{
    auditAction: "connection.binding.disabled",
    failures: #[#{ kind: "audit_unavailable", statusCode: 503, code: "AUDIT_UNAVAILABLE", publicDetail: "Connection binding audit could not be persisted." }],
  })
  @apigen.auditPayload(TargetConnectionAdministrationAuditPayload)
  disableTargetConnectionBinding(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
    @header("Idempotency-Key") idempotencyKey: IdempotencyKey,
  ): OkJson<TargetConnectionBindingResponse> | CommonErrors;

  @summary("Get redacted target connection runtime health")
  @route("/{connection}/health")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_READ" })
  @extension("x-leapview-object-scope", "connection")
  getTargetConnectionBindingHealth(
    @path project: ResourceId,
    @path target: TargetConnectionIdentifier,
    @path @apigen.target connection: LogicalConnectionId,
  ): OkJson<TargetConnectionBindingHealthResponse> | CommonErrors;
}
