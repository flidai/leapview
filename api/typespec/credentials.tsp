using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Credential;

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CredentialDraftAuditPayload {
  @apigen.auditInternal
  version_id: string;

  @apigen.auditInternal
  purpose: string;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "INVALID_CREDENTIAL_DRAFT", publicDetail: "Credential draft request is invalid." })
model InvalidCredentialDraftFailure {}

@apigen.failureDefinition(#{ kind: "unauthorized", statusCode: 403, code: "CREDENTIAL_DRAFT_FORBIDDEN", publicDetail: "Credential draft access is forbidden." })
model ForbiddenCredentialDraftFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "CREDENTIAL_DRAFT_NOT_FOUND", publicDetail: "Credential draft was not found." })
model CredentialDraftNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "CREDENTIAL_DRAFT_CONFLICT", publicDetail: "Credential draft conflicts with stored state." })
model CredentialDraftConflictFailure {}

@apigen.failureDefinition(#{ kind: "provider_unavailable", statusCode: 503, code: "CREDENTIAL_SERVICE_UNAVAILABLE", publicDetail: "Credential draft service is unavailable." })
model CredentialDraftServiceUnavailableFailure {}

@minLength(36)
@maxLength(36)
@pattern("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
scalar CredentialDraftVersionId extends string;

@minValue(1)
@maxValue(100)
scalar CredentialDraftListLimit extends int32;

@doc("Encrypted draft metadata only. Credential values, ciphertext, provider references and digests are never returned.")
model CredentialDraftResponse {
  versionId: CredentialDraftVersionId;
  createdAt: utcDateTime;
  state: "draft";
}

model CredentialDraftListResponse {
  items: CredentialDraftResponse[];
  nextBeforeVersionId?: CredentialDraftVersionId;
}

model CredentialVersionDependencyResponse { kind: string; id: string; }
@doc("Retained logical references to this exact credential version. retired_local denies future use by this instance only; it does not revoke upstream credentials or remove encrypted backup history.")
model CredentialVersionStatusResponse {
  versionId: CredentialDraftVersionId;
  state: "available" | "retired_local";
  retiredAt?: utcDateTime;
  dependencies: CredentialVersionDependencyResponse[];
  moreDependencies: boolean;
}
model CredentialVersionRetireRequest {}
@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CredentialVersionRetiredAuditPayload {
  @apigen.auditInternal version_id: string;
  @apigen.auditInternal state: string;
}

@doc("The secret field values accepted for the connector resolved from the connection binding. The encoded JSON request body is limited to 32 KiB and the typed secret bundle to 16 KiB. Names and values are secret input and are never reflected in responses, audit payloads or logs.")
model CredentialDraftSaveRequest {
  @apigen.sensitivity("secret")
  fields: Record<string>;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CredentialValidationAuditPayload {
  @apigen.auditInternal receipt_id: string;
  @apigen.auditInternal version_id: string;
  @apigen.auditInternal binding_id: string;
  @apigen.auditInternal binding_revision: int64;
  @apigen.auditInternal configuration_digest: string;
  @apigen.auditInternal expires_at: utcDateTime;
}

@apigen.failureDefinition(#{ kind: "credential_invalid", statusCode: 422, code: "CREDENTIAL_VALIDATION_FAILED", publicDetail: "Credential validation failed." })
model CredentialValidationFailedFailure {}

model CredentialValidationRequest {
  @minValue(1)
  expectedBindingRevision: int64;
}

scalar CredentialValidationReceiptId extends CredentialDraftVersionId;

@doc("Observation from an isolated probe of this saved version and binding revision. Validation does not activate the credential or permit revoking the previous credential. The receipt expires after five minutes and can become stale sooner if configuration or authority changes.")
model CredentialValidationResponse {
  receiptId: CredentialValidationReceiptId;
  versionId: CredentialDraftVersionId;
  bindingRevision: int64;
  validatedAt: utcDateTime;
  expiresAt: utcDateTime;
  state: "validated";
}

@route("/projects/{project}/targets/{target}/connection-bindings/{connection}/credential-drafts")
@tag("Credentials")
interface ConnectionCredentialDrafts {
  @summary("Inspect exact credential version dependencies")
  @route("/{version}/status")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  getCredentialVersionStatus(@path project: ResourceId, @path target: ResourceId, @path @apigen.target connection: ResourceId, @path version: CredentialDraftVersionId): OkJson<CredentialVersionStatusResponse> | CommonErrors;

  @summary("Retire an unreferenced credential version locally")
  @route("/{version}/retire")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialDraftFailure)
  @apigen.failsWith(ForbiddenCredentialDraftFailure)
  @apigen.failsWith(CredentialDraftNotFoundFailure)
  @apigen.failsWith(CredentialDraftConflictFailure)
  @apigen.failsWith(CredentialDraftServiceUnavailableFailure)
  @apigen.command(#{ auditAction: "credential.version.retired_local", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(CredentialVersionRetiredAuditPayload)
  @apigen.ui("credential.version.retire")
  retireCredentialVersion(@path project: ResourceId, @path target: ResourceId, @path @apigen.target connection: ResourceId, @path version: CredentialDraftVersionId, @body body: CredentialVersionRetireRequest): OkJson<CredentialVersionStatusResponse> | CommonErrors;

  @summary("List connection credential draft metadata")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.read", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  listConnectionCredentialDrafts(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @query limit?: CredentialDraftListLimit,
    @query beforeVersionId?: CredentialDraftVersionId,
  ): OkJson<CredentialDraftListResponse> | CommonErrors;

  @summary("Save a connection credential draft")
  @doc("Stores a new encrypted, immutable draft only. Saving does not validate, publish or activate the credential. A lost response can leave an unreturned draft; explicit resubmission may create a second draft.")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialDraftFailure)
  @apigen.failsWith(ForbiddenCredentialDraftFailure)
  @apigen.failsWith(CredentialDraftNotFoundFailure)
  @apigen.failsWith(CredentialDraftConflictFailure)
  @apigen.failsWith(CredentialDraftServiceUnavailableFailure)
  @apigen.command(#{
    auditAction: "credential.draft.saved",
    guarantee: "transactional",
    nonReplayable: true,
  })
  @apigen.auditPayload(CredentialDraftAuditPayload)
  @apigen.ui("credential.draft.save")
  saveCredentialDraft(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @body body: CredentialDraftSaveRequest,
  ): CreatedJson<CredentialDraftResponse> | CommonErrors;

  @summary("Read connection credential draft metadata")
  @route("/{version}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.read", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  getConnectionCredentialDraft(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path version: CredentialDraftVersionId,
  ): OkJson<CredentialDraftResponse> | CommonErrors;

  @summary("Validate a saved connection credential draft")
  @doc("Requires both connection.manage and connection.use on the exact connection. Probes only the saved PostgreSQL password version against the server-owned binding at expectedBindingRevision; it never changes running pools. Every explicit submission performs a fresh probe; Idempotency-Key is forbidden. A lost response may leave a receipt without returning it. Success means validated, not in use.")
  @route("/{version}/validate")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialDraftFailure)
  @apigen.failsWith(ForbiddenCredentialDraftFailure)
  @apigen.failsWith(CredentialDraftNotFoundFailure)
  @apigen.failsWith(CredentialDraftConflictFailure)
  @apigen.failsWith(CredentialDraftServiceUnavailableFailure)
  @apigen.failsWith(CredentialValidationFailedFailure)
  @apigen.command(#{ auditAction: "credential.draft.validated", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(CredentialValidationAuditPayload)
  @apigen.ui("credential.draft.validate")
  validateCredentialDraft(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path version: CredentialDraftVersionId,
    @body body: CredentialValidationRequest,
  ): OkJson<CredentialValidationResponse> | CommonErrors;

}

@route("/agent/credential-versions")
@extension("x-leapview-object-scope", "instance")
@tag("Credentials")
interface AgentCredentialVersions {
  @summary("Inspect agent credential version dependencies")
  @route("/{version}")
  @get
  @extension("x-leapview-object-scope", "instance")
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.settings.update", resolver: "instance" })
  getAgentCredentialVersionStatus(@path version: CredentialDraftVersionId): OkJson<CredentialVersionStatusResponse> | CommonErrors;

  @summary("Retire an unreferenced agent credential version locally")
  @route("/{version}/retire")
  @post
  @extension("x-leapview-object-scope", "instance")
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.settings.update", resolver: "instance" })
  @apigen.failsWith(InvalidCredentialDraftFailure)
  @apigen.failsWith(ForbiddenCredentialDraftFailure)
  @apigen.failsWith(CredentialDraftNotFoundFailure)
  @apigen.failsWith(CredentialDraftConflictFailure)
  @apigen.failsWith(CredentialDraftServiceUnavailableFailure)
  @apigen.command(#{ auditAction: "credential.agent_version.retired_local", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(CredentialVersionRetiredAuditPayload)
  @apigen.ui("credential.agent-version.retire")
  retireAgentCredentialVersion(@path version: CredentialDraftVersionId, @body body: CredentialVersionRetireRequest): OkJson<CredentialVersionStatusResponse> | CommonErrors;
}

scalar CredentialActivationOperationId extends CredentialDraftVersionId;

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CredentialActivationAuditPayload {
  @apigen.auditInternal operation_id: string;
  @apigen.auditInternal version_id: string;
  @apigen.auditInternal state: string;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "INVALID_CREDENTIAL_ACTIVATION", publicDetail: "Credential activation request is invalid." })
model InvalidCredentialActivationFailure {}
@apigen.failureDefinition(#{ kind: "unauthorized", statusCode: 403, code: "CREDENTIAL_ACTIVATION_FORBIDDEN", publicDetail: "Credential activation access is forbidden." })
model ForbiddenCredentialActivationFailure {}
@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "CREDENTIAL_ACTIVATION_NOT_FOUND", publicDetail: "Credential activation was not found." })
model CredentialActivationNotFoundFailure {}
@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "CREDENTIAL_ACTIVATION_CONFLICT", publicDetail: "Credential activation conflicts with stored state." })
model CredentialActivationConflictFailure {}
@apigen.failureDefinition(#{ kind: "provider_unavailable", statusCode: 503, code: "CREDENTIAL_ACTIVATION_UNAVAILABLE", publicDetail: "Credential activation service is unavailable." })
model CredentialActivationUnavailableFailure {}

@doc("Durable operation metadata only. Committed means the exact publication pointer is durable; only completed with runtimeReady=true confirms that the replacement runtime is ready. No credential values, ciphertext or provider references are returned.")
model CredentialActivationResponse {
  operationId: CredentialActivationOperationId;
  versionId: CredentialDraftVersionId;
  state: "preparing" | "prepared" | "switching" | "committed" | "completed" | "aborted";
  @minValue(1) bindingRevision: int64;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  runtimeReady: boolean;
  candidateId?: string;
  generationId?: string;
  publicationId?: string;
}

@doc("The caller retains this operation UUID to resolve a lost response. Reusing it requires the same immutable version, validation receipt and binding revision. Destination, ownership and publication inputs are resolved by the server.")
model CredentialActivationRequest {
  operationId: CredentialActivationOperationId;
  receiptId: CredentialValidationReceiptId;
  @minValue(1) expectedBindingRevision: int64;
}

@doc("Before commit, supply a fresh receipt for the same version and current binding. After commit the receipt may be omitted; recovery restores only that exact committed publication. Retry is explicit and rechecks current caller authority.")
model CredentialActivationRetryRequest {
  receiptId?: CredentialValidationReceiptId;
}

model CredentialActivationAbortRequest {}

@route("/projects/{project}/targets/{target}/connection-bindings/{connection}")
@tag("Credentials")
interface ConnectionCredentialActivations {
  @summary("Prepare activation of a validated connection credential")
  @doc("Requires connection.manage and connection.use on the exact connection. Persists the operation and prepares its exact candidate without claiming runtime use. Idempotency-Key is forbidden; retain operationId and query status after a lost response.")
  @route("/credential-drafts/{version}/activate")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialActivationFailure)
  @apigen.failsWith(ForbiddenCredentialActivationFailure)
  @apigen.failsWith(CredentialActivationNotFoundFailure)
  @apigen.failsWith(CredentialActivationConflictFailure)
  @apigen.failsWith(CredentialActivationUnavailableFailure)
  @apigen.command(#{ auditAction: "credential.activation.requested", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(CredentialActivationAuditPayload)
  @apigen.ui("credential.activation.prepare")
  startCredentialActivation(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path version: CredentialDraftVersionId,
    @body body: CredentialActivationRequest,
  ): AcceptedJson<CredentialActivationResponse> | OkJson<CredentialActivationResponse> | CommonErrors;

  @summary("Read a connection credential activation")
  @route("/credential-activations/{operation}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.read", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  getCredentialActivation(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path operation: CredentialActivationOperationId,
  ): OkJson<CredentialActivationResponse> | CommonErrors;

  @summary("Continue or recover an exact connection credential activation")
  @route("/credential-activations/{operation}/retry")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialActivationFailure)
  @apigen.failsWith(ForbiddenCredentialActivationFailure)
  @apigen.failsWith(CredentialActivationNotFoundFailure)
  @apigen.failsWith(CredentialActivationConflictFailure)
  @apigen.failsWith(CredentialActivationUnavailableFailure)
  @apigen.command(#{ auditAction: "credential.activation.retried", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(CredentialActivationAuditPayload)
  @apigen.ui("credential.activation.retry")
  retryCredentialActivation(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path operation: CredentialActivationOperationId,
    @body body: CredentialActivationRetryRequest,
  ): AcceptedJson<CredentialActivationResponse> | OkJson<CredentialActivationResponse> | CommonErrors;

  @summary("Abort an uncommitted connection credential activation")
  @doc("Aborting does not reverse a committed publication. Inspect status and use retry to complete recovery after commit.")
  @route("/credential-activations/{operation}/abort")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialActivationFailure)
  @apigen.failsWith(ForbiddenCredentialActivationFailure)
  @apigen.failsWith(CredentialActivationNotFoundFailure)
  @apigen.failsWith(CredentialActivationConflictFailure)
  @apigen.failsWith(CredentialActivationUnavailableFailure)
  @apigen.command(#{ auditAction: "credential.activation.aborted", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(CredentialActivationAuditPayload)
  @apigen.ui("credential.activation.abort")
  abortCredentialActivation(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path operation: CredentialActivationOperationId,
    @body body: CredentialActivationAbortRequest,
  ): OkJson<CredentialActivationResponse> | CommonErrors;
}

scalar FirstSourcePreparationId extends CredentialActivationOperationId;

@minLength(71)
@maxLength(71)
@pattern("^sha256:[0-9a-f]{64}$")
scalar FirstSourcePreparationDigest extends string;

@doc("Explicit retained source and plan intent for one validated first-source credential. Publisher and source owner are the authenticated actor. This operation currently requires a browser session; bearer requests fail closed. Preparation does not grant publisher permissions or replace independent review.")
model FirstSourcePreparationRequest {
  preparationId: FirstSourcePreparationId;
  receiptId: CredentialValidationReceiptId;
  sourceDigest: FirstSourcePreparationDigest;
  sourceAttestationDigest: FirstSourcePreparationDigest;
  @minLength(1) @maxLength(255) planIdempotencyKey: string;
  @minValue(1) expectedTargetRevision: int64;
}

model FirstSourcePreparationRenewRequest {
  receiptId: CredentialValidationReceiptId;
}

@doc("Immutable non-secret preparation metadata. This identifies exact plan intent, not an active credential or publication permission.")
model FirstSourcePreparationResponse {
  preparationId: FirstSourcePreparationId;
  intentDigest: FirstSourcePreparationDigest;
  planRequestDigest: FirstSourcePreparationDigest;
  createdAt: utcDateTime;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model FirstSourcePreparationAuditPayload {
  @apigen.auditInternal preparationId: string;
  @apigen.auditInternal intentDigest: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model FirstSourcePreparationRenewAuditPayload {
  @apigen.auditInternal preparationId: string;
  @apigen.auditInternal receiptId: string;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "INVALID_FIRST_SOURCE_PREPARATION", publicDetail: "First-source preparation request is invalid." })
model InvalidFirstSourcePreparationFailure {}
@apigen.failureDefinition(#{ kind: "unauthorized", statusCode: 403, code: "FIRST_SOURCE_PREPARATION_FORBIDDEN", publicDetail: "First-source preparation is forbidden." })
model ForbiddenFirstSourcePreparationFailure {}
@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "FIRST_SOURCE_PREPARATION_NOT_FOUND", publicDetail: "First-source preparation evidence was not found." })
model FirstSourcePreparationNotFoundFailure {}
@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "FIRST_SOURCE_PREPARATION_CONFLICT", publicDetail: "First-source preparation conflicts with stored state." })
model FirstSourcePreparationConflictFailure {}
@apigen.failureDefinition(#{ kind: "provider_unavailable", statusCode: 503, code: "FIRST_SOURCE_PREPARATION_UNAVAILABLE", publicDetail: "First-source preparation is unavailable." })
model FirstSourcePreparationUnavailableFailure {}

@route("/projects/{project}/targets/{target}/connection-bindings/{connection}")
@tag("Credentials")
interface FirstSourceCredentialPreparations {
  @summary("Prepare a validated first-source credential for an exact source plan")
  @doc("Requires the current browser session and exact connection.manage and connection.use. Retain preparationId for explicit retry after a lost response. Idempotency-Key is forbidden; planIdempotencyKey binds the later normal code_change plan. Bearer calls fail closed.")
  @route("/credential-drafts/{version}/prepare-first-source")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidFirstSourcePreparationFailure)
  @apigen.failsWith(ForbiddenFirstSourcePreparationFailure)
  @apigen.failsWith(FirstSourcePreparationNotFoundFailure)
  @apigen.failsWith(FirstSourcePreparationConflictFailure)
  @apigen.failsWith(FirstSourcePreparationUnavailableFailure)
  @apigen.command(#{ auditAction: "credential.first_source.prepared", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(FirstSourcePreparationAuditPayload)
  @apigen.ui("credential.first-source.prepare")
  prepareFirstSourceCredential(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path version: CredentialDraftVersionId,
    @body body: FirstSourcePreparationRequest,
  ): OkJson<FirstSourcePreparationResponse> | CommonErrors;

  @summary("Renew proof for an exact uncommitted first-source preparation")
  @doc("Requires the original actor's current browser session, exact manage/use and a fresh receipt for the same version, binding and configuration. Original source, publisher and plan intent remain immutable. Terminal operations cannot be revived. Idempotency-Key is forbidden; bearer calls fail closed.")
  @route("/first-source-preparations/{preparation}/renew")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidFirstSourcePreparationFailure)
  @apigen.failsWith(ForbiddenFirstSourcePreparationFailure)
  @apigen.failsWith(FirstSourcePreparationNotFoundFailure)
  @apigen.failsWith(FirstSourcePreparationConflictFailure)
  @apigen.failsWith(FirstSourcePreparationUnavailableFailure)
  @apigen.command(#{ auditAction: "credential.first_source.receipt_renewed", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(FirstSourcePreparationRenewAuditPayload)
  @apigen.ui("credential.first-source.renew")
  renewFirstSourceCredentialPreparation(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path preparation: FirstSourcePreparationId,
    @body body: FirstSourcePreparationRenewRequest,
  ): OkJson<FirstSourcePreparationResponse> | CommonErrors;
}
